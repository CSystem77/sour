package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cfoust/sour/svc/cluster/clients"
	"github.com/cfoust/sour/svc/cluster/servers"

	"github.com/rs/zerolog/log"
)

func (server *Cluster) GivePrivateMatchHelp(ctx context.Context, client clients.Client, gameServer *servers.GameServer) {
	// TODO this is broken; the context is from the timeout for the command so it never runs again
	tick := time.NewTicker(30 * time.Second)

	message := fmt.Sprintf("This is your private server. Have other players join by saying '#join %s' in any Sour server.", gameServer.Id)

	for {
		gameServer.Mutex.Lock()
		numClients := gameServer.NumClients
		gameServer.Mutex.Unlock()

		log.Info().Msgf("warning: %d", numClients)

		if numClients < 2 {
			clients.SendServerMessage(client, message)
		} else {
			return
		}

		select {
		case <-tick.C:
			continue
		case <-ctx.Done():
			return
		}
	}
}

func (server *Cluster) RunCommand(ctx context.Context, command string, client clients.Client, state *clients.ClientState) (handled bool, response string, err error) {
	logger := log.With().Uint16("client", client.Id()).Str("command", command).Logger()
	logger.Info().Msg("running command")

	args := strings.Split(command, " ")

	if len(args) == 0 {
		return false, "", errors.New("invalid command")
	}

	switch args[0] {
	case "creategame":
		server.createMutex.Lock()
		defer server.createMutex.Unlock()

		lastCreate, hasLastCreate := server.lastCreate[client.Host()]
		if hasLastCreate && (time.Now().Sub(lastCreate)) < CREATE_SERVER_COOLDOWN {
			return true, "", errors.New("too soon since last server create")
		}

		existingServer, hasExistingServer := server.hostServers[client.Host()]
		if hasExistingServer {
			server.manager.RemoveServer(existingServer)
		}

		logger.Info().Msg("starting server")

		presetName := ""
		if len(args) > 1 {
			presetName = args[1]
		}

		gameServer, err := server.manager.NewServer(server.serverCtx, presetName)
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to create server")
			return true, "", errors.New("failed to create server")
		}

		logger = logger.With().Str("server", gameServer.Reference()).Logger()

		err = gameServer.StartAndWait(server.serverCtx)
		if err != nil {
			logger.Fatal().Err(err).Msg("server failed to start")
			return true, "", errors.New("server failed to start")
		}

		server.lastCreate[client.Host()] = time.Now()
		server.hostServers[client.Host()] = gameServer

		state.Mutex.Lock()

		if client.Type() == clients.ClientTypeENet {
			go server.GivePrivateMatchHelp(server.serverCtx, client, state.Server)
		}

		state.Mutex.Unlock()
		return server.RunCommand(ctx, fmt.Sprintf("join %s", gameServer.Id), client, state)

	case "join":
		if len(args) != 2 {
			return true, "", errors.New("join takes a single argument")
		}

		target := args[1]


		state.Mutex.Lock()
		if state.Server != nil && state.Server.IsReference(target) {
			logger.Info().Msg("client already connected to target")
			state.Mutex.Unlock()
			break
		}
		state.Mutex.Unlock()

		for _, gameServer := range server.manager.Servers {
			if !gameServer.IsReference(target) || !gameServer.IsRunning() {
				continue
			}

			err := server.Clients.ConnectClient(gameServer, client)
			if err != nil {
				return true, "", err
			}

			return true, "", nil
		}

		logger.Warn().Msgf("could not find server: %s", target)

	case "queue":
		server.matches.Queue(client)
	}

	return false, "", nil
}

func (server *Cluster) RunCommandWithTimeout(ctx context.Context, command string, client clients.Client, state *clients.ClientState) (handled bool, response string, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)

	resultChannel := make(chan clients.CommandResult)

	defer cancel()

	go func() {
		handled, response, err := server.RunCommand(ctx, command, client, state)
		resultChannel <- clients.CommandResult{
			Handled:  handled,
			Err:      err,
			Response: response,
		}
	}()

	select {
	case result := <-resultChannel:
		return result.Handled, result.Response, result.Err
	case <-ctx.Done():
		cancel()
		return false, "", errors.New("command timed out")
	}

}
