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
		clients := gameServer.NumClients
		gameServer.Mutex.Unlock()

		log.Info().Msgf("warning: %d", clients)

		if clients < 2 {
			server.SendServerMessage(client, message)
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


func (server *Cluster) RunCommand(ctx context.Context, command string, client clients.Client, state *clients.ClientState) (string, error) {
	logger := log.With().Uint16("client", client.Id()).Str("command", command).Logger()
	logger.Info().Msg("running command")

	args := strings.Split(command, " ")

	if len(args) == 0 {
		return "", errors.New("invalid command")
	}

	switch args[0] {
	case "creategame":
		server.createMutex.Lock()
		defer server.createMutex.Unlock()

		lastCreate, hasLastCreate := server.lastCreate[client.Host()]
		if hasLastCreate && (time.Now().Sub(lastCreate)) < CREATE_SERVER_COOLDOWN {
			return "", errors.New("too soon since last server create")
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
			return "", errors.New("failed to create server")
		}

		logger = logger.With().Str("server", gameServer.Reference()).Logger()

		go gameServer.Start(server.serverCtx)
		go server.PollServer(server.serverCtx, gameServer)

		tick := time.NewTicker(250 * time.Millisecond)
		for {
			status := gameServer.GetStatus()
			if status == servers.ServerOK {
				logger.Info().Msg("server ok")
				break
			}

			select {
			case <-ctx.Done():
				return "", errors.New("server start timed out")
			case <-tick.C:
				continue
			}
		}

		server.lastCreate[client.Host()] = time.Now()
		server.hostServers[client.Host()] = gameServer

		state.Mutex.Lock()

		// Automatically connect clients to their servers
		if client.Type() == clients.ClientTypeWS && state.Server == nil {
			state.Mutex.Unlock()
			return gameServer.Id, nil
		}

		if client.Type() == clients.ClientTypeENet {
			go server.GivePrivateMatchHelp(ctx, client, state.Server)
		}

		state.Mutex.Unlock()
		return server.RunCommand(ctx, fmt.Sprintf("join %s", gameServer.Id), client, state)

	case "join":
		if len(args) != 2 {
			return "", errors.New("join takes a single argument")
		}

		target := args[1]

		state.Mutex.Lock()
		defer state.Mutex.Unlock()

		if state.Server != nil && state.Server.IsReference(target) {
			logger.Info().Msg("client already connected to target")
			break
		}

		for _, gameServer := range server.manager.Servers {
			if !gameServer.IsReference(target) || gameServer.Status != servers.ServerOK {
				continue
			}

			if state.Server != nil {
				state.Server.SendDisconnect(client.Id())
			}

			state.Server = gameServer

			logger.Info().Str("server", gameServer.Reference()).
				Msg("client connecting to server")

			gameServer.SendConnect(client.Id())

			client.Connect()
			return "", nil
		}

		logger.Warn().Msgf("could not find server: %s", target)
	}

	return "", nil
}

func (server *Cluster) RunCommandWithTimeout(ctx context.Context, command string, client clients.Client, state *clients.ClientState) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)

	resultChannel := make(chan clients.CommandResult)

	go func() {
		response, err := server.RunCommand(ctx, command, client, state)
		resultChannel <- clients.CommandResult{
			Err:      err,
			Response: response,
		}
	}()

	select {
	case result := <-resultChannel:
		cancel()
		return result.Response, result.Err
	case <-ctx.Done():
		cancel()
		return "", errors.New("command timed out")
	}
}
