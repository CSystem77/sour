package clients

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"

	"github.com/cfoust/sour/pkg/game"
	"github.com/cfoust/sour/svc/cluster/auth"
	"github.com/cfoust/sour/svc/cluster/ingress"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// The status of the client's connection to their game server.
type ClientStatus uint8

const (
	ClientStatusConnecting = iota
	ClientStatusConnected
	ClientStatusDisconnected
)

type Intercept struct {
	From chan game.GamePacket
	To   chan game.GamePacket
}

type Client struct {
	Id    uint16

	// Whether the client is connected (or connecting) to a game server
	Status    ClientStatus

	Connection ingress.Connection

	// The ID of the client on the Sauer server
	Num int32
	// Each time a player dies, they're given a number (probably for
	// anti-hacking?)
	LifeSequence int

	// True when the user is loading the map
	delayMessages bool
	messageQueue  []string

	Authentication chan *auth.AuthUser

	Intercept Intercept

	// XXX This is nasty but to make the API nice, Clients have to be able
	// to see the list of clients. This could/should be refactored someday.
	manager *ClientManager

	mutex sync.Mutex
}

func (c *Client) Logger() zerolog.Logger {
	return log.With().Uint16("client", c.Id).Logger()
}

func (c *Client) ReceiveAuthentication() <-chan *auth.AuthUser {
	// WS clients do their own auth (for now)
	if c.Connection.Type() == ingress.ClientTypeWS {
		return c.Connection.ReceiveAuthentication()
	}

	return c.Authentication
}

func (c *Client) GetStatus() ClientStatus {
	c.mutex.Lock()
	status := c.Status
	c.mutex.Unlock()
	return status
}

func (c *Client) GetClientNum() int32 {
	c.mutex.Lock()
	num := c.Num
	c.mutex.Unlock()
	return num
}

func (c *Client) GetLifeSequence() int {
	c.mutex.Lock()
	num := c.LifeSequence
	c.mutex.Unlock()
	return num
}

func (c *Client) DelayMessages() {
	c.mutex.Lock()
	c.delayMessages = true
	c.mutex.Unlock()
}

func (c *Client) RestoreMessages() {
	c.mutex.Lock()
	c.delayMessages = false
	c.mutex.Unlock()
	c.sendQueuedMessages()
}

func (c *Client) sendQueuedMessages() {
	c.mutex.Lock()
	for _, message := range c.messageQueue {
		c.sendMessage(message)
	}
	c.messageQueue = make([]string, 0)
	c.mutex.Unlock()
}


func (c *Client) sendMessage(message string) {
	packet := game.Packet{}
	packet.PutInt(int32(game.N_SERVMSG))
	packet.PutString(message)
	c.Connection.Send(game.GamePacket{
		Channel: 1,
		Data:    packet,
	})
}

func (c *Client) Send(packet game.GamePacket) <-chan bool {
	c.Intercept.To <- packet
	return c.Connection.Send(packet)
}

func (c *Client) ReceiveIntercept() (<-chan game.GamePacket, <-chan game.GamePacket) {
	return c.Intercept.To, c.Intercept.From
}

func (c *Client) SendMessage(message string) {
	c.mutex.Lock()
	if c.delayMessages {
		c.messageQueue = append(c.messageQueue, message)
	} else {
		c.sendMessage(message)
	}
	c.mutex.Unlock()
}

func (c *Client) SendServerMessage(message string) {
	c.SendMessage(fmt.Sprintf("%s %s", game.Yellow("sour"), message))
}

type ClientManager struct {
	State      map[*Client]struct{}
	mutex      sync.Mutex
	newClients chan *Client
}

func NewClientManager() *ClientManager {
	return &ClientManager{
		State:      make(map[*Client]struct{}),
		newClients: make(chan *Client, 16),
	}
}

func (c *ClientManager) newClientID() (uint16, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for attempts := 0; attempts < math.MaxUint16; attempts++ {
		number, _ := rand.Int(rand.Reader, big.NewInt(math.MaxUint16))
		truncated := uint16(number.Uint64())

		taken := false
		for client := range c.State {
			if client.Id == truncated {
				taken = true
			}
		}
		if taken {
			continue
		}

		return truncated, nil
	}

	return 0, errors.New("Failed to assign client ID")
}

func (c *ClientManager) AddClient(networkClient ingress.Connection) error {
	id, err := c.newClientID()
	if err != nil {
		return err
	}

	client := Client{
		Id:               id,
		Connection:       networkClient,
		Status:           ClientStatusDisconnected,
		manager:          c,
		delayMessages:    false,
		messageQueue:     make([]string, 0),
		Authentication:   make(chan *auth.AuthUser, 1),
		Intercept: Intercept{
			To:   make(chan game.GamePacket, 1000),
			From: make(chan game.GamePacket, 1000),
		},
	}

	c.mutex.Lock()
	c.State[&client] = struct{}{}
	c.mutex.Unlock()

	c.newClients <- &client

	return nil
}

func (c *ClientManager) RemoveClient(networkClient ingress.Connection) {
	c.mutex.Lock()

	for client := range c.State {
		if client.Connection != networkClient {
			continue
		}

		delete(c.State, client)
		break
	}

	c.mutex.Unlock()
}

func (c *ClientManager) ReceiveClients() <-chan *Client {
	return c.newClients
}

func (c *ClientManager) FindClient(id uint16) *Client {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for client := range c.State {
		if client.Id != uint16(id) {
			continue
		}

		return client
	}

	return nil
}
