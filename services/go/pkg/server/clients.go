package server

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/cfoust/sour/pkg/game/protocol"
	"github.com/cfoust/sour/pkg/server/game"
	"github.com/cfoust/sour/pkg/server/protocol/cubecode"
	"github.com/cfoust/sour/pkg/server/protocol/disconnectreason"
	"github.com/cfoust/sour/pkg/server/protocol/playerstate"
	"github.com/cfoust/sour/pkg/server/protocol/role"

	"github.com/sasha-s/go-deadlock"
)

type ClientManager struct {
	clients []*Client
	mutex   deadlock.RWMutex
}

func (cm *ClientManager) Add(sessionId uint32, outgoing Outgoing) *Client {
	cm.mutex.Lock()
	cn := uint32(len(cm.clients))
	c := NewClient(cn, sessionId, outgoing)
	cm.clients = append(cm.clients, c)
	cm.mutex.Unlock()
	return c
}

func (cm *ClientManager) GetClientByCN(cn uint32) *Client {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	if int(cn) < 0 || int(cn) >= len(cm.clients) {
		return nil
	}

	return cm.clients[cn]
}

func (cm *ClientManager) GetClientByID(sessionId uint32) *Client {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	for _, client := range cm.clients {
		if client.SessionID == sessionId {
			return client
		}
	}

	return nil
}

func (cm *ClientManager) FindClientByName(name string) *Client {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	name = strings.ToLower(name)
	for _, c := range cm.clients {
		if strings.Contains(c.Name, name) {
			return c
		}
	}
	return nil
}

// Send a packet to a client's team, but not the client himself, over the specified channel.
func (cm *ClientManager) SendToTeam(c *Client, messages ...protocol.Message) {
	excludeSelfAndOtherTeams := func(_c *Client) bool {
		return _c == c || _c.Team != c.Team
	}
	cm.broadcast(excludeSelfAndOtherTeams, messages...)
}

// Sends a packet to all clients currently in use.
func (cm *ClientManager) Broadcast(messages ...protocol.Message) {
	cm.broadcast(nil, messages...)
}

func (cm *ClientManager) broadcast(exclude func(*Client) bool, messages ...protocol.Message) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	for _, c := range cm.clients {
		if exclude != nil && exclude(c) {
			continue
		}

		c.Send(messages...)
	}
}

func exclude(c *Client) func(*Client) bool {
	return func(_c *Client) bool {
		return _c == c
	}
}

func (cm *ClientManager) Relay(from *Client, messages ...protocol.Message) {
	cm.broadcast(exclude(from), messages...)
}

// Sends 'welcome' information to a newly joined client like map, mode, time left, other players, etc.
func (s *Server) SendWelcome(c *Client) {
	messages := []protocol.Message{
		protocol.Welcome{},
		protocol.MapChange{
			Name:     s.Map,
			Mode:     int(s.GameMode.ID()),
			HasItems: s.GameMode.NeedsMapInfo(),
		},
		// time left in this round
		protocol.TimeUp{int(s.Clock.TimeLeft() / time.Second)},
	}

	if pickupMode, ok := s.GameMode.(game.PickupMode); ok && !s.GameMode.NeedsMapInfo() {
		messages = append(messages, pickupMode.PickupsInitPacket())
	}

	// send list of clients which have privilege higher than PRIV_NONE and their respecitve privilege level
	privileged, empty := s.PrivilegedUsersPacket()
	if !empty {
		messages = append(messages, privileged)
	}

	if s.Clock.Paused() {
		messages = append(messages, protocol.PauseGame{true, -1})
	}

	if teamMode, ok := s.GameMode.(game.TeamMode); ok {
		teamInfo := protocol.TeamInfo{}

		teamMode.ForEachTeam(func(t *game.Team) {
			if t.Frags > 0 {
				teamInfo.Teams = append(teamInfo.Teams, protocol.Team{t.Name, t.Frags})
			}
		})

		messages = append(messages, teamInfo)
	}

	// tell the client what team he was put in by the server
	messages = append(messages, protocol.SetTeam{
		Client: int(c.CN),
		Team:   c.Team.Name,
		Reason: -1,
	})

	// tell the client how to spawn (what health, what armour, what weapons, what ammo, etc.)
	if c.State == playerstate.Spectator {
		messages = append(messages, protocol.Spectator{
			Client:     int(c.CN),
			Spectating: true,
		})
	} else {
		// TODO: handle spawn delay (e.g. in ctf modes)
		messages = append(messages, protocol.SpawnState{
			Client:      int(c.CN),
			EntityState: c.ToWire(),
		})
	}

	// send other players' state (frags, flags, etc.)
	resume := protocol.Resume{}
	for _, client := range s.Clients.clients {
		if client != c {
			resume.Clients = append(
				resume.Clients,
				protocol.ClientState{
					Id:          int(client.CN),
					State:       int(client.State),
					Frags:       client.Frags,
					Flags:       client.Flags,
					Deaths:      client.Deaths,
					Quadmillis:  int(client.QuadTimer.TimeLeft() / time.Millisecond),
					EntityState: client.ToWire(),
				},
			)
		}
	}
	messages = append(messages, resume)

	// send other client's state (name, team, playermodel)
	for _, client := range s.Clients.clients {
		if client != c {
			messages = append(messages, protocol.InitClient{
				int(client.CN), client.Name, client.Team.Name, int(client.Model),
			})
		}
	}

	c.Send(messages...)
}

// Tells other clients that the client disconnected, giving a disconnect reason in case it's not a normal leave.
func (cm *ClientManager) Disconnect(c *Client, reason disconnectreason.ID) {
	cm.Relay(c, protocol.ClientDisconnected{int(c.CN)})

	msg := ""
	if reason != disconnectreason.None {
		msg = fmt.Sprintf("%s disconnected because: %s", cm.UniqueName(c), reason)
		cm.Relay(c, protocol.ServerMessage{msg})
	} else {
		msg = fmt.Sprintf("%s disconnected", cm.UniqueName(c))
	}
	log.Println(cubecode.SanitizeString(msg))

	cm.mutex.Lock()
	newClients := make([]*Client, 0)
	for _, client := range cm.clients {
		if client == c {
			continue
		}
		newClients = append(newClients, client)
	}
	cm.clients = newClients
	cm.mutex.Unlock()
}

// Informs all other clients that a client joined the game.
func (cm *ClientManager) InformOthersOfJoin(c *Client) {
	cm.Relay(c, protocol.InitClient{
		int(c.CN), c.Name, c.Team.Name, int(c.Model),
	})

	if c.State == playerstate.Spectator {
		cm.Relay(c, protocol.Spectator{
			int(c.CN), true,
		})
	}
}

func (s *Server) MapChange() {
	s.Clients.ForEach(func(c *Client) {
		c.Player.PlayerState.Reset()
		if c.State == playerstate.Spectator {
			return
		}
		s.Spawn(c)
		c.Send(protocol.SpawnState{
			Client:      int(c.CN),
			EntityState: c.ToWire(),
		})
	})
}

func (cm *ClientManager) PrivilegedUsers() (privileged []*Client) {
	cm.ForEach(func(c *Client) {
		if c.Role > role.None {
			privileged = append(privileged, c)
		}
	})
	return
}

func (s *Server) PrivilegedUsersPacket() (protocol.Message, bool) {
	message := protocol.CurrentMaster{
		MasterMode: int(s.MasterMode),
	}

	s.Clients.ForEach(func(c *Client) {
		if c.Role > role.None {
			message.Clients = append(message.Clients, protocol.ClientPrivilege{
				int(c.CN),
				int(c.Role),
			})
		}
	})

	return message, len(message.Clients) == 0
}

// Returns the number of connected clients.
func (cm *ClientManager) NumberOfClientsConnected() (n int) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	return len(cm.clients)
}

func (cm *ClientManager) ForEach(do func(c *Client)) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	for _, c := range cm.clients {
		do(c)
	}
}

func (cm *ClientManager) UniqueName(c *Client) string {
	unique := true
	cm.ForEach(func(_c *Client) {
		if _c != c && _c.Name == c.Name {
			unique = false
		}
	})

	if !unique {
		return c.Name + cubecode.Magenta(" ("+strconv.FormatUint(uint64(c.CN), 10)+")")
	}
	return c.Name
}
