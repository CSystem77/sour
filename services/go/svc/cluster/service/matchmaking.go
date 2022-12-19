package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cfoust/sour/pkg/game"
	"github.com/cfoust/sour/pkg/game/messages"
	"github.com/cfoust/sour/svc/cluster/clients"
	"github.com/cfoust/sour/svc/cluster/servers"
	"github.com/rs/zerolog/log"
)

type QueuedClient struct {
	joinTime time.Time
	client   clients.Client
}

type Matchmaker struct {
	manager    *servers.ServerManager
	clients    *clients.ClientManager
	queue      []QueuedClient
	queueEvent chan bool
	mutex      sync.Mutex
}

func NewMatchmaker(manager *servers.ServerManager, clients *clients.ClientManager) *Matchmaker {
	return &Matchmaker{
		queue:      make([]QueuedClient, 0),
		queueEvent: make(chan bool, 0),
		manager:    manager,
		clients:    clients,
	}
}

func (m *Matchmaker) Queue(client clients.Client) {
	log.Info().Uint16("client", client.Id()).Msg("queued for dueling")
	clients.SendServerMessage(client, "you are now queued for dueling")
	m.mutex.Lock()
	m.queue = append(m.queue, QueuedClient{
		client:   client,
		joinTime: time.Now(),
	})
	m.mutex.Unlock()
	m.queueEvent <- true
}

func (m *Matchmaker) Poll(ctx context.Context) {
	updateTicker := time.NewTicker(10 * time.Second)

	for {
		// Check to see if there are any matches we can arrange
		m.mutex.Lock()

		// First prune the list of any clients that are gone
		cleaned := make([]QueuedClient, 0)
		for _, queued := range m.queue {
			if queued.client.NetworkStatus() == clients.ClientNetworkStatusDisconnected {
				log.Info().Uint16("client", queued.client.Id()).Msg("pruning disconnected client")
				continue
			}
			cleaned = append(cleaned, queued)
		}
		m.queue = cleaned

		// Then look to see if we can make any matches
		matched := make(map[clients.Client]bool, 0)
		for _, queuedA := range m.queue {
			// We may have already matched this queued
			// note: can this actually occur?
			if _, ok := matched[queuedA.client]; ok {
				continue
			}

			for _, queuedB := range m.queue {
				// Same here
				if _, ok := matched[queuedB.client]; ok {
					continue
				}
				if queuedA == queuedB {
					continue
				}

				matched[queuedA.client] = true
				matched[queuedB.client] = true

				// We have a match!
				go m.Duel(ctx, queuedA.client, queuedB.client)
			}

			since := time.Now().Sub(queuedA.joinTime)
			clients.SendServerMessage(queuedA.client, fmt.Sprintf("You have been queued for %s. Say #leavequeue to leave.", since.String()))
		}

		// Remove the matches we made from the queue
		cleaned = make([]QueuedClient, 0)
		for _, queued := range m.queue {
			if _, ok := matched[queued.client]; ok {
				continue
			}
			cleaned = append(cleaned, queued)
		}
		m.queue = cleaned

		m.mutex.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-m.queueEvent:
		case <-updateTicker.C:
		}
	}
}

// Do a period of uninterrupted gameplay, like the warmup or main "struggle" sections.
func (m *Matchmaker) DoSession(ctx context.Context, duration time.Duration) {
	sessionCtx, cancelSession := context.WithTimeout(ctx, duration)
	defer cancelSession()
	select {
	case <-ctx.Done():
		cancelSession()
		return
	case <-sessionCtx.Done():
	}
}

func (m *Matchmaker) DoCountdown(ctx context.Context, seconds int, message func(string)) {
	tick := time.NewTicker(1 * time.Second)
	count := seconds

	for {
		select {
		case <-tick.C:
			log.Info().Msgf("countdown tick %d", count)
			message(fmt.Sprintf("%d", count))
			if count == 0 {
				return
			}
			count--
		case <-ctx.Done():
			log.Info().Msg("countdown context canceled")
			return
		}
	}
}

func abs(x, y int) int {
	if x < y {
		return y - x
	}
	return x - y
}

func (m *Matchmaker) Duel(ctx context.Context, clientA clients.Client, clientB clients.Client) {
	logger := log.With().Uint16("clientA", clientA.Id()).Uint16("clientB", clientB.Id()).Logger()

	logger.Info().Msg("initiating 1v1")

	matchContext, cancelMatch := context.WithCancel(ctx)
	defer cancelMatch()

	// If any client disconnects from the CLUSTER, end the match
	for _, client := range []clients.Client{clientA, clientB} {
		go func(client clients.Client) {
			select {
			case <-matchContext.Done():
				return
			case <-client.SessionContext().Done():
				logger.Info().Msgf("client %d disconnected from cluster, ending match", client.Id())
				cancelMatch()
				return
			}
		}(client)
	}

	go func() {
		select {
		case <-matchContext.Done():
			logger.Info().Msg("the duel ended")
			return
		}
	}()

	broadcast := func(text string) {
		clients.SendServerMessage(clientA, text)
		clients.SendServerMessage(clientB, text)
	}

	failure := func() {
		broadcast(game.Red("error starting match server"))
	}

	broadcast(game.Green("Found a match!"))
	broadcast("starting match server")

	gameServer, err := m.manager.NewServer(ctx, "1v1")
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create server")
		failure()
		return
	}

	logger = logger.With().Str("server", gameServer.Reference()).Logger()

	err = gameServer.StartAndWait(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("server failed to start")
		failure()
		return
	}

	gameServer.SendCommand("pausegame 1")

	if matchContext.Err() != nil {
		return
	}

	// Move the clients to the new server
	for _, client := range []clients.Client{clientA, clientB} {
		state := m.clients.GetState(client)

		// Store previous server
		oldServer := state.GetServer()

		go func(client clients.Client, oldServer *servers.GameServer) {
			select {
			case <-matchContext.Done():
				// When the match is done (regardless of result) attempt to move
				m.clients.ConnectClient(oldServer, client)
				return
			case <-state.ServerSessionContext().Done():
				logger.Info().Msgf("client %d disconnected from server, ending match", client.Id())
				// If any client disconnects from the SERVER, end the match
				cancelMatch()
				return
			}
		}(client, oldServer)

		m.clients.ConnectClient(gameServer, client)
		err = m.clients.WaitUntilConnected(ctx, client)
		if err != nil {
			logger.Fatal().Err(err).Msg("client failed to connect")
			failure()
		}

	}

	if matchContext.Err() != nil {
		return
	}

	gameServer.SendCommand("pausegame 0")

	// Start with a warmup
	broadcast(game.Blue("WARMUP: 30 seconds remaining"))
	m.DoSession(matchContext, 10*time.Second)
	broadcast(game.Blue("WARMUP OVER"))
	gameServer.SendCommand("resetplayers 1")

	broadcasts := gameServer.BroadcastSubscribe()
	defer gameServer.BroadcastUnsubscribe(broadcasts)

	scoreA := 0
	scoreB := 0
	var scoreMutex sync.Mutex

	go func() {
		for {
			select {
			case msg := <-broadcasts:
				if msg.Type() == game.N_DIED {
					died := msg.Contents().(*messages.Died)

					if died.Client == died.Killer {
						continue
					}

					scoreMutex.Lock()
					// should be A?
					if died.Client == 0 {
						logger.Info().Err(err).Msg("client B killed A")
						scoreB = died.Frags
					} else if died.Client == 1 {
						logger.Info().Err(err).Msg("client A killed B")
						scoreA = died.Frags
					}
					scoreMutex.Unlock()
				}
			case <-matchContext.Done():
				return
			}
		}
	}()

	gameServer.SendCommand("pausegame 1")
	m.DoCountdown(matchContext, 5, broadcast)
	gameServer.SendCommand("pausegame 0")
	broadcast(game.Red("GO"))

	m.DoSession(matchContext, 10*time.Second)

	// You have to win by three points from where overtime started
	for {
		scoreMutex.Lock()
		overtimeA := scoreA
		overtimeB := scoreB
		scoreMutex.Unlock()

		if abs(overtimeA, overtimeB) >= 3 {
			break
		}

		broadcast("OVERTIME")
		gameServer.SendCommand("resetplayers 0")

		gameServer.SendCommand("pausegame 1")
		m.DoCountdown(matchContext, 5, broadcast)
		gameServer.SendCommand("pausegame 0")

		broadcast(game.Red("GO"))
		m.DoSession(matchContext, 10*time.Second)
	}

	logger.Info().Msgf("match ended %d:%d", scoreA, scoreB)
}
