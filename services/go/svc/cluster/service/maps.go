package service

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/cfoust/sour/pkg/game"
	"github.com/cfoust/sour/svc/cluster/assets"
	"github.com/cfoust/sour/svc/cluster/clients"
	//"github.com/rs/zerolog/log"

	"github.com/repeale/fp-go/option"
)

type SendStatus byte

const (
	SendStatusInitialized SendStatus = iota
	SendStatusDownloading
	SendStatusMoved
)

type SendState struct {
	Status SendStatus
	Mutex  sync.Mutex
	Client *clients.Client
	Maps   *assets.MapFetcher
	Sender *MapSender
	Map    string
}

func (s *SendState) SetStatus(status SendStatus) {
	s.Mutex.Lock()
	s.Status = status
	s.Mutex.Unlock()
}

func (s *SendState) SendClient(data []byte) {
	s.Client.Connection.Send(game.GamePacket{
		Channel: 1,
		Data:    data,
	})
}

func (s *SendState) Send() error {
	client := s.Client
	logger := client.Logger()
	ctx := client.ServerSessionContext()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// First we send a dummy map
	s.SetStatus(SendStatusDownloading)

	client.SendServerMessage("downloading map")
	p := game.Packet{}
	p.Put(
		game.N_MAPCHANGE,
		game.MapChange{
			Name:     "sending",
			Mode:     game.MODE_COOP,
			HasItems: 0,
		},
	)
	s.SendClient(p)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	desktopURL := s.Maps.FindDesktopURL(s.Map)
	if opt.IsNone(desktopURL) {
		// How?
		return fmt.Errorf("could not find map URL")
	}

	mapPath := filepath.Join(s.Sender.workingDir, assets.GetURLBase(desktopURL.Value))

	err := assets.DownloadFile(
		desktopURL.Value,
		mapPath,
	)
	if err != nil {
		return err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	logger.Info().Msgf("downloaded desktop map to %s", mapPath)

	client.SendServerMessage("downloaded map")

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

func (m *MapSender) SendMap(ctx context.Context, client *clients.Client, mapName string) {
	logger := client.Logger()
	logger.Info().Str("map", mapName).Msg("sending map")
	state := &SendState{
		Status: SendStatusInitialized,
		Client: client,
		Map:    mapName,
		Maps:   m.Maps,
		Sender: m,
	}

	m.Mutex.Lock()
	m.Clients[client] = state
	m.Mutex.Unlock()

	go state.Send()
}

func (m *MapSender) Shutdown() {
	os.RemoveAll(m.workingDir)
}
