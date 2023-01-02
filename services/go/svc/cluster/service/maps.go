package service

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cfoust/sour/pkg/game"
	"github.com/cfoust/sour/pkg/maps"
	"github.com/cfoust/sour/svc/cluster/assets"
	"github.com/cfoust/sour/svc/cluster/clients"

	"github.com/repeale/fp-go/option"
	"github.com/rs/zerolog/log"
)

func MakeDownloadMap(demoName string) ([]byte, error) {
	gameMap := maps.NewMap()
	gameMap.Vars["cloudlayer"] = maps.StringVariable("")
	gameMap.Vars["skyboxcolour"] = maps.IntVariable(0)

	// First, request the "demo" in its entirety.
	fileName := demoName[:20]
	script := fmt.Sprintf(`
can_teleport_1 = [
demodir sour
getdemo 0 %s
can_teleport_1 = []
]
can_teleport_2 = [
addzip sour/%s.dmo
demodir demo
can_teleport_2 = []
]
say a
`, fileName, fileName)

	log.Warn().Msgf("maptitle len=%d", len(script))
	gameMap.Vars["maptitle"] = maps.StringVariable(script)

	gameMap.Entities = append(gameMap.Entities,
		maps.Entity{
			Type:  game.EntityTypeTeleport,
			Attr3: 1,
			Position: maps.Vector{
				X: 512 + 10,
				Y: 512 + 10,
				Z: 512,
			},
		},
		maps.Entity{
			Type:  game.EntityTypeTeleport,
			Attr3: 2,
			Position: maps.Vector{
				X: 512 - 10,
				Y: 512 - 10,
				Z: 512,
			},
		},
	)

	mapBytes, err := gameMap.EncodeOGZ()
	if err != nil {
		return mapBytes, err
	}

	return mapBytes, nil
}

type SendState struct {
	Mutex  sync.Mutex
	Client *clients.Client
	Maps   *assets.MapFetcher
	Sender *MapSender
	Path   string
	Map    string

	userAccepted  chan bool
	demoRequested chan int
}

func (s *SendState) SendClient(data []byte, channel int) <-chan bool {
	return s.Client.Connection.Send(game.GamePacket{
		Channel: uint8(channel),
		Data:    data,
	})
}

func (s *SendState) SendClientSync(data []byte, channel int) error {
	if !<-s.SendClient(data, channel) {
		return fmt.Errorf("client never acknowledged message")
	}
	return nil
}

func (s *SendState) MoveClient(x float64, y float64) error {
	p := game.Packet{}
	err := p.Put(
		game.N_POS,
		uint(s.Client.GetClientNum()),
		game.PhysicsState{
			LifeSequence: s.Client.GetLifeSequence(),
			O: game.Vec{
				X: x,
				Y: y,
				Z: 512 + 14,
			},
		},
	)
	if err != nil {
		return err
	}
	s.SendClient(p, 0)
	return nil
}

func (s *SendState) SendPause(state bool) error {
	p := game.Packet{}
	p.Put(
		game.N_PAUSEGAME,
		state,
		s.Client.GetClientNum(),
	)
	s.SendClient(p, 1)
	return nil
}

func (s *SendState) SendDemo(tag int) {
	s.demoRequested <- tag
}

func (s *SendState) TriggerSend() {
	s.userAccepted <- true
}

func (s *SendState) Send() error {
	client := s.Client

	sendCtx, cancelSend := context.WithCancel(client.ServerSessionContext())
	defer cancelSend()

	logger := client.Logger()

	if sendCtx.Err() != nil {
		return sendCtx.Err()
	}

	s.SendPause(true)

	p := game.Packet{}
	p.Put(
		game.N_MAPCHANGE,
		game.MapChange{
			Name:     "sending",
			Mode:     game.MODE_COOP,
			HasItems: 0,
		},
	)
	s.SendClient(p, 1)

	if sendCtx.Err() != nil {
		return sendCtx.Err()
	}

	map_ := s.Maps.FindMap(s.Map)
	if opt.IsNone(map_) {
		// How?
		return fmt.Errorf("could not find map")
	}

	fakeMap, err := MakeDownloadMap(map_.Value.Map.Bundle)
	if err != nil {
		logger.Error().Err(err).Msgf("failed to make map")
		return err
	}

	time.Sleep(1 * time.Second)
	p = game.Packet{}
	p.Put(game.N_SENDMAP)
	p = append(p, fakeMap...)
	err = s.SendClientSync(p, 2)
	if err != nil {
		return err
	}

	desktopURL := map_.Value.GetDesktopURL()
	mapPath := filepath.Join(s.Sender.workingDir, assets.GetURLBase(desktopURL))
	s.Path = mapPath
	err = assets.DownloadFile(
		desktopURL,
		mapPath,
	)
	if err != nil {
		return err
	}

	if sendCtx.Err() != nil {
		return sendCtx.Err()
	}

	logger.Info().Msgf("downloaded desktop map to %s", mapPath)

	client.SendServerMessage("You are missing this map. Please run '/do $maptitle' to download it.")

	doCtx, cancelDo := context.WithTimeout(sendCtx, 30*time.Second)
	defer cancelDo()

	select {
	case <-s.userAccepted:
	case <-doCtx.Done():
		return doCtx.Err()
	}

	s.Client.GetServer().SendCommand(fmt.Sprintf("forcerespawn %d", s.Client.GetClientNum()))
	time.Sleep(1 * time.Second)
	s.MoveClient(512+10, 512+10)
	time.Sleep(1 * time.Second)
	// so physics runs
	s.SendPause(false)

	var tag int
	select {
	case request := <-s.demoRequested:
		tag = request
	case <-sendCtx.Done():
		return sendCtx.Err()
	}

	file, err := os.Open(s.Path)
	defer file.Close()
	if err != nil {
		return err
	}

	buffer, err := io.ReadAll(file)
	if err != nil {
		return err
	}

	p = game.Packet{}
	p.Put(
		game.N_SENDDEMO,
		tag,
		len(buffer),
	)
	p = append(p, buffer...)
	err = s.SendClientSync(p, 2)
	if err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)

	if sendCtx.Err() != nil {
		return sendCtx.Err()
	}

	// Then load the demo
	s.SendPause(true)
	s.MoveClient(512-10, 512-10)
	time.Sleep(500 * time.Millisecond)

	if sendCtx.Err() != nil {
		return sendCtx.Err()
	}

	s.SendPause(false)
	return nil
}

type MapSender struct {
	Clients    map[*clients.Client]*SendState
	Maps       *assets.MapFetcher
	Mutex      sync.Mutex
	workingDir string
}

func NewMapSender(maps *assets.MapFetcher) *MapSender {
	return &MapSender{
		Clients: make(map[*clients.Client]*SendState),
		Maps:    maps,
	}
}

func (m *MapSender) Start() error {
	tempDir, err := ioutil.TempDir("", "maps")
	if err != nil {
		return err
	}

	m.workingDir = tempDir

	err = os.MkdirAll(tempDir, 0755)
	if err != nil {
		return err
	}

	return nil
}

// Whether a map is being sent to this client.
func (m *MapSender) IsHandling(client *clients.Client) bool {
	m.Mutex.Lock()
	_, handling := m.Clients[client]
	m.Mutex.Unlock()
	return handling
}

func (m *MapSender) SendDemo(ctx context.Context, client *clients.Client, tag int) {
	m.Mutex.Lock()
	state, handling := m.Clients[client]
	m.Mutex.Unlock()

	if !handling {
		return
	}

	state.SendDemo(tag)
}

func (m *MapSender) TriggerSend(ctx context.Context, client *clients.Client) {
	m.Mutex.Lock()
	state, handling := m.Clients[client]
	m.Mutex.Unlock()

	if !handling {
		return
	}

	state.TriggerSend()
}

func (m *MapSender) SendMap(ctx context.Context, client *clients.Client, mapName string) {
	logger := client.Logger()
	logger.Info().Str("map", mapName).Msg("sending map")
	state := &SendState{
		Client:        client,
		Map:           mapName,
		Maps:          m.Maps,
		Sender:        m,
		userAccepted:  make(chan bool, 1),
		demoRequested: make(chan int, 1),
	}

	m.Mutex.Lock()
	m.Clients[client] = state
	m.Mutex.Unlock()
	server := client.GetServer()

	out := make(chan error)
	go func() {
		out <- state.Send()
	}()
	go func() {
		select {
		case <-client.ServerSessionContext().Done():
			return
		case err := <-out:
			if err != nil {
				logger.Error().Err(err).Msg("failed to download map")
				return
			}

			m.Mutex.Lock()
			delete(m.Clients, client)
			m.Mutex.Unlock()

			// Now we can reconnect the user to their server
			client.DisconnectFromServer()
			client.ConnectToServer(server, false, false)
		}
	}()
}

func (m *MapSender) Shutdown() {
	os.RemoveAll(m.workingDir)
}
