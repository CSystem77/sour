package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/cfoust/sour/svc/cluster/assets"
	"github.com/cfoust/sour/svc/cluster/config"
	"github.com/cfoust/sour/svc/cluster/ingress"
	"github.com/cfoust/sour/svc/cluster/servers"
	"github.com/cfoust/sour/svc/cluster/service"
	"github.com/cfoust/sour/svc/cluster/auth"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	DEBUG = false
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if DEBUG {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Warn().Msg("debug logging enabled")
	}

	sourConfig, err := config.GetSourConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load sour configuration, please specify one with the SOUR_CONFIG environment variable")
	}

	clusterConfig := sourConfig.Cluster

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	maps := assets.NewMapFetcher()
	err = maps.FetchIndices(clusterConfig.Assets)

	if err != nil {
		log.Fatal().Err(err).Msg("failed to load assets")
	}

	serverManager := servers.NewServerManager(maps, clusterConfig.ServerDescription, clusterConfig.Presets)
	cluster := service.NewCluster(ctx, serverManager, clusterConfig)

	err = serverManager.Start()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start server manager")
	}

	var discord *auth.DiscordService = nil
	discordSettings := sourConfig.Discord
	if discordSettings.Enabled {
		log.Info().Msg("initializing Discord authentication")
		discord = auth.NewDiscordService(
			discordSettings.Id,
			discordSettings.Secret,
			discordSettings.RedirectURI,
		)
	}

	wsIngress := ingress.NewWSIngress(cluster.Clients, discord)

	enet := make([]*ingress.ENetIngress, 0)
	infoServices := make([]*servers.ServerInfoService, 0)

	cluster.StartServers(ctx)

	for _, enetConfig := range clusterConfig.Ingress.Desktop {
		enetIngress := ingress.NewENetIngress(cluster.Clients)
		enetIngress.Serve(enetConfig.Port)
		enetIngress.InitialCommand = fmt.Sprintf("join %s", enetConfig.Target)
		go enetIngress.Poll(ctx)

		if enetConfig.ServerInfo.Enabled {
			serverManager.Mutex.Lock()
			for _, server := range serverManager.Servers {
				if server.Reference() != enetConfig.Target {
					continue
				}

				serverInfo := servers.NewServerInfoService(server)

				if enetConfig.ServerInfo.Cluster {
					serverInfo = servers.NewServerInfoService(cluster)
				}

				err := serverInfo.Serve(ctx, enetConfig.Port+1, enetConfig.ServerInfo.Master)
				if err != nil {
					log.Fatal().Err(err).Msg("failed to start server info service")
				}
				infoServices = append(infoServices, serverInfo)
			}
			serverManager.Mutex.Unlock()
		}

		enet = append(enet, enetIngress)
	}
	go cluster.PollClients(ctx)
	go cluster.PollDuels(ctx)

	errc := make(chan error, 1)
	go func() {
		errc <- wsIngress.Serve(ctx, clusterConfig.Ingress.Web.Port)
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	signal.Notify(sigs, os.Kill)

	select {
	case err := <-errc:
		log.Printf("failed to serve: %v", err)
	case sig := <-sigs:
		log.Printf("terminating: %v", sig)
	}

	wsIngress.Shutdown(ctx)
	for _, enetIngress := range enet {
		enetIngress.Shutdown()
	}
	for _, infoService := range infoServices {
		infoService.Shutdown()
	}
	cluster.Shutdown()
}
