package ingress

import (
	"context"
	"sync"
	"time"

	"github.com/cfoust/sour/pkg/enet"
	"github.com/cfoust/sour/pkg/game"
	"github.com/cfoust/sour/svc/cluster/auth"

	"github.com/rs/zerolog/log"
)

type PacketACK struct {
	Packet game.GamePacket
	Done   chan bool
}

type ENetClient struct {
	peer   *enet.Peer
	host   *enet.Host
	status NetworkStatus

	context context.Context
	cancel  context.CancelFunc

	toClient       chan PacketACK
	toServer       chan game.GamePacket
	commands       chan ClusterCommand
	authentication chan *auth.AuthUser
	disconnect     chan bool
}

func NewENetClient() *ENetClient {
	return &ENetClient{
		status:         NetworkStatusConnected,
		toClient:       make(chan PacketACK, CLIENT_MESSAGE_LIMIT),
		toServer:       make(chan game.GamePacket, CLIENT_MESSAGE_LIMIT),
		commands:       make(chan ClusterCommand, CLIENT_MESSAGE_LIMIT),
		authentication: make(chan *auth.AuthUser),
		disconnect:     make(chan bool, 1),
	}
}

func (c *ENetClient) Host() string {
	peer := c.peer
	if peer != nil && peer.Address != nil {
		ip := peer.Address.IP.To4()
		if ip != nil {
			return ip.String()
		}
	}
	return ""
}

func (c *ENetClient) Connect(name string, isHidden bool, shouldCopy bool) {
}

func (c *ENetClient) ServerChanged(target string) {
}

func (c *ENetClient) SessionContext() context.Context {
	return c.context
}

func (c *ENetClient) NetworkStatus() NetworkStatus {
	return c.status
}

func (c *ENetClient) Destroy() {
	c.status = NetworkStatusDisconnected
}

func (c *ENetClient) Type() ClientType {
	return ClientTypeENet
}

func (c *ENetClient) Send(packet game.GamePacket) <-chan bool {
	done := make(chan bool, 1)
	c.toClient <- PacketACK{
		Packet: packet,
		Done:   done,
	}
	return done
}

func (c *ENetClient) SendGlobalChat(message string) {
	packet := game.Packet{}
	packet.PutInt(int32(game.N_SERVMSG))
	packet.PutString(message)
	c.Send(game.GamePacket{
		Channel: 1,
		Data:    packet,
	})
}

func (c *ENetClient) ReceivePackets() <-chan game.GamePacket {
	return c.toServer
}

func (c *ENetClient) ReceiveCommands() <-chan ClusterCommand {
	return c.commands
}

func (c *ENetClient) ReceiveAuthentication() <-chan *auth.AuthUser {
	return c.authentication
}

func (c *ENetClient) ReceiveDisconnect() <-chan bool {
	return c.disconnect
}

func (c *ENetClient) Poll(ctx context.Context) {
	for {
		select {
		case packetACK := <-c.toClient:
			packet := packetACK.Packet
			done := c.peer.Send(packet.Channel, packet.Data)

			// Go wait for the ACK and pass it on
			go func() {
				timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				select {
				case result := <-done:
					packetACK.Done <- result
					return
				case <-timeout.Done():
					packetACK.Done <- false
					return
				}
			}()
			continue
		case <-ctx.Done():
			return
		}
	}
}

func (c *ENetClient) Disconnect(reason int, message string) {
	c.cancel()
	c.host.Disconnect(c.peer, enet.ID(reason))
}

type ENetIngress struct {
	newClients chan Connection
	// Run when a client joins
	InitialCommand string
	clients        map[*ENetClient]struct{}
	host           *enet.Host
	mutex          sync.Mutex
}

func NewENetIngress(newClients chan Connection) *ENetIngress {
	return &ENetIngress{
		newClients: newClients,
		clients:    make(map[*ENetClient]struct{}),
	}
}

func (server *ENetIngress) Serve(port int) error {
	host, err := enet.NewHost("", port)
	if err != nil {
		return err
	}
	server.host = host
	return nil
}

func (server *ENetIngress) FindClientForPeer(peer *enet.Peer) *ENetClient {
	var target *ENetClient = nil

	server.mutex.Lock()
	for client := range server.clients {
		if client.peer == nil || peer.CPeer != client.peer.CPeer {
			continue
		}

		target = client
		break
	}
	server.mutex.Unlock()

	return target
}

func (server *ENetIngress) AddClient(s *ENetClient) {
	server.mutex.Lock()
	server.clients[s] = struct{}{}
	server.mutex.Unlock()
}

func (server *ENetIngress) RemoveClient(client *ENetClient) {
	server.mutex.Lock()
	delete(server.clients, client)
	server.mutex.Unlock()
}

func (server *ENetIngress) Poll(ctx context.Context) {
	events := server.host.Service()

	for {
		select {
		case event := <-events:
			switch event.Type {
			case enet.EventTypeConnect:
				ctx, cancel := context.WithCancel(ctx)

				client := NewENetClient()
				client.peer = event.Peer
				client.context = ctx
				client.cancel = cancel
				client.host = server.host

				server.newClients <- client

				server.AddClient(client)

				log.Info().Msg("client joined (desktop)")

				if len(server.InitialCommand) > 0 {
					client.commands <- ClusterCommand{
						Command: server.InitialCommand,
					}
				}

				go client.Poll(ctx)

				break

			case enet.EventTypeReceive:
				target := server.FindClientForPeer(event.Peer)

				if target == nil {
					continue
				}

				target.toServer <- game.GamePacket{
					Channel: event.ChannelID,
					Data:    event.Packet.Data,
				}

				break
			case enet.EventTypeDisconnect:
				target := server.FindClientForPeer(event.Peer)

				if target == nil {
					continue
				}

				target.cancel()
				server.RemoveClient(target)
				target.disconnect <- true
				break
			}
		case <-ctx.Done():
			return
		}
	}

}

func (server *ENetIngress) Shutdown() {
	server.host.Shutdown()
}
