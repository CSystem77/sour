package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cfoust/sour/pkg/game"
	C "github.com/cfoust/sour/pkg/game/constants"
	"github.com/cfoust/sour/pkg/game/io"
	P "github.com/cfoust/sour/pkg/game/protocol"
	S "github.com/cfoust/sour/pkg/server"
	"github.com/cfoust/sour/svc/cluster/ingress"
	"github.com/cfoust/sour/svc/cluster/servers"

	"github.com/go-redis/redis/v9"
	"github.com/rs/zerolog/log"
)

func (server *Cluster) NotifyClientChange(ctx context.Context, user *User, joined bool) {
	userServer := user.GetServer()
	name := user.GetFormattedName()
	serverName := user.GetServerName()

	event := "join"
	if !joined {
		event = "leave"
	}

	// To users on another server
	message := fmt.Sprintf("%s: %s (%s)", event, name, serverName)

	server.Users.Mutex.RLock()
	for _, other := range server.Users.Users {
		if other == user {
			continue
		}

		otherServer := other.GetServer()

		// On the same server, we can just use chat
		if userServer == otherServer {
			continue
		}
		other.RawMessage(message)
	}
	server.Users.Mutex.RUnlock()
}

func (server *Cluster) NotifyNameChange(ctx context.Context, user *User, oldName string) {
	newName := user.GetName()

	if newName == oldName {
		return
	}

	clientServer := user.GetServer()
	serverName := user.GetServerName()
	message := fmt.Sprintf("%s now known as %s [%s]", oldName, newName, serverName)

	server.Users.Mutex.RLock()
	for _, other := range server.Users.Users {
		if other == user {
			continue
		}

		otherServer := other.GetServer()

		// On the same server, we can just use chat
		if clientServer == otherServer {
			continue
		}
		other.RawMessage(message)
	}
	server.Users.Mutex.RUnlock()
}

func (c *Cluster) AnnounceInServer(ctx context.Context, server *servers.GameServer, message string) {
	c.Users.Mutex.RLock()

	serverUsers, ok := c.Users.Servers[server]
	if !ok {
		return
	}

	for _, user := range serverUsers {
		user.Message(message)
	}

	c.Users.Mutex.RUnlock()
}

func (server *Cluster) ForwardGlobalChat(ctx context.Context, sender *User, message string) {
	server.Users.Mutex.RLock()
	senderServer := sender.GetServer()
	senderNum := sender.GetClientNum()

	name := sender.GetFormattedName()

	// To users who share the same server
	sameMessage := fmt.Sprintf("%s: %s", name, game.Green(message))

	serverName := senderServer.GetFormattedReference()

	// To users on another server
	otherMessage := fmt.Sprintf("%s [%s]: %s", name, serverName, game.Green(message))

	for _, user := range server.Users.Users {
		if user == sender {
			continue
		}

		otherServer := user.GetServer()

		if user.Connection.Type() == ingress.ClientTypeWS {
			ws := user.Connection.(*ingress.WSClient)
			ws.SendGlobalChat(sameMessage)
			continue
		}

		// On the same server, we can just use chat
		if senderServer == otherServer {
			// We lose the formatting, but that's OK
			user.Send(
				P.ClientPacket{senderNum, 0},
				P.Text{message},
			)
			continue
		}

		user.RawMessage(otherMessage)
	}
	server.Users.Mutex.RUnlock()
}

func (c *Cluster) HandleTeleport(ctx context.Context, user *User, source int) {
	logger := user.Logger()

	space := user.GetSpace()
	server := user.GetServer()
	if server != nil && space != nil {
		links, err := space.GetLinks(ctx)
		if err != nil {
			return
		}

		entities := server.GetEntities()
		if source < 0 || source >= len(entities) {
			return
		}

		teleport := entities[source]

		for _, link := range links {
			if link.ID == uint8(teleport.Attr1) {
				logger.Info().Msgf("teleported to %s", link.Destination)
				go c.RunCommandWithTimeout(
					user.Ctx(),
					fmt.Sprintf("go %s", link.Destination),
					user,
				)
			}
		}
	}
}

func (c *Cluster) PollFromMessages(ctx context.Context, user *User) {
	userCtx := user.Ctx()

	//passthrough := func(channel uint8, message P.Message) {
		//server := user.GetServer()
		//if server != nil {
			//server.SendData(
				//user.Id,
				//uint32(channel),
				//message.Data(),
			//)
		//}
	//}

	chats := user.From.Intercept(P.N_TEXT)
	blockConnecting := user.From.InterceptWith(func(code P.MessageCode) bool {
		return !P.IsConnectingMessage(code)
	})
	connects := user.From.Intercept(P.N_CONNECT)
	teleports := user.From.Intercept(P.N_TELEPORT)
	edits := user.From.InterceptWith(P.IsOwnerOnly)
	crcs := user.From.Intercept(P.N_MAPCRC)
	votes := user.From.Intercept(P.N_MAPVOTE)

	for {
		logger := user.Logger()

		select {
		case <-userCtx.Done():
			return
		case msg := <-votes.Receive():
			space := user.GetSpace()
			if space == nil {
				msg.Pass()
				continue
			}

			preset := space.PresetSpace
			if preset == nil || !preset.VotingCreates {
				msg.Pass()
				continue
			}

			vote := msg.Message.(P.MapVote)
			if vote.Mode < 0 || vote.Mode >= len(MODE_NAMES) {
				msg.Pass()
				continue
			}

			msg.Drop()

			err := c.RunOnBehalf(
				ctx,
				fmt.Sprintf(
					"creategame %s %s",
					MODE_NAMES[vote.Mode],
					vote.Map,
				),
				user,
			)
			if err != nil {
				logger.Error().Err(err).Msg("failed to create game from vote")
			}
		case msg := <-crcs.Receive():
			msg.Pass()
			user.RestoreMessages()
			crc := msg.Message.(P.MapCRC)
			// The client does not have the map
			if crc.Crc == 0 {
				go func() {
					err := c.SendMap(ctx, user, crc.Map)
					if err != nil {
						logger.Warn().Err(err).Msg("failed to send map to client")
					}
				}()
			}
		case msg := <-edits.Receive():
			isOwner, err := user.IsOwner(ctx)
			if err != nil {
				msg.Drop()
				continue
			}

			space := user.GetSpace()
			if space != nil {
				canEditSpace := isOwner || space.IsOpenEdit()
				if !canEditSpace {
					user.ConnectToSpace(space.Server, space.GetID())
					user.Message("you cannot edit this space.")
					msg.Drop()
					continue
				}
			}

			server := user.GetServer()
			// For now, users can't edit on named servers (ie the lobby)
			if server != nil && server.Alias != "" {
				user.Connect(server)
				user.Message("you cannot edit this server.")
				msg.Drop()
				continue
			}
			msg.Pass()
		case msg := <-teleports.Receive():
			message := msg.Message
			teleport := message.(P.Teleport)
			c.HandleTeleport(ctx, user, teleport.Source)
			msg.Pass()
		case msg := <-connects.Receive():
			message := msg.Message
			connect := message.(P.Connect)
			description := connect.AuthDescription
			name := connect.AuthName
			msg.Pass()

			if user.Connection.Type() != ingress.ClientTypeENet {
				continue
			}

			go func() {
				user.Mutex.RLock()
				wasGreeted := user.wasGreeted
				user.Mutex.RUnlock()
				if wasGreeted {
					return
				}

				if description != c.authDomain || c.auth == nil {
					user.Authentication <- nil
					return
				}

				if !user.IsLoggedIn() {
					err := c.DoAuthChallenge(ctx, user, name)
					if err != nil {
						logger.Warn().Err(err).Msgf("failed to log in")
					}
				}
			}()
		case msg := <-blockConnecting.Receive():
			// Skip messages that aren't allowed while the
			// client is connecting, otherwise the server
			// (rightfully) disconnects us. This solves a
			// race condition when switching servers.
			status := user.GetStatus()
			if status == UserStatusConnecting {
				msg.Drop()
				continue
			}
			msg.Pass()
		case msg := <-chats.Receive():
			message := msg.Message
			text := message.(P.Text).Text

			msg.Drop()

			if len(text) >= C.MAXSTRLEN {
				removed := len(text) - C.MAXSTRLEN
				text = text[:C.MAXSTRLEN]
				user.Message(game.Red(
					fmt.Sprintf("your message was too long; we cut off the last %d characters", removed),
				))
			}

			if !strings.HasPrefix(text, "#") {
				// We do our own chat, don't pass on to the server
				c.ForwardGlobalChat(userCtx, user, text)
				continue
			}

			msg.Pass()

			// TODO
			//command := text[1:]

			// Only send this packet after we've checked
			// whether the cluster should handle it
			//go func() {
			//handled, response, err := c.RunCommandWithTimeout(userCtx, command, user)

			//if !handled {
			//passthrough(msg.Channel, message)
			//msg.Pass()
			//return
			//}

			//if err != nil {
			//logger.Error().Err(err).Str("command", command).Msg("user command failed")
			//user.Message(game.Red(err.Error()))
			//return
			//} else if len(response) > 0 {
			//user.Message(response)
			//return
			//}

			//if command == "help" {
			//passthrough(msg.Channel, message)
			//}
			//}()
			continue
		}
	}
}

func (c *Cluster) PollToMessages(ctx context.Context, user *User) {
	userCtx := user.Ctx()
	serverInfo := user.To.Intercept(P.N_SERVINFO)

	for {
		select {
		case <-userCtx.Done():
			return
		case msg := <-serverInfo.Receive():
			// Inject the auth domain to N_SERVINFO so the
			// client sends us N_CONNECT with their name
			// field filled
			info := msg.Message.(P.ServerInfo)

			user.Mutex.RLock()
			wasGreeted := user.wasGreeted
			user.Mutex.RUnlock()
			if !wasGreeted {
				info.Domain = c.authDomain
			}

			user.Mutex.Lock()
			user.lastDescription = info.Description
			user.Mutex.Unlock()

			msg.Replace(info)
		}
	}
}

func (c *Cluster) PollUser(ctx context.Context, user *User) {
	commands := user.Connection.ReceiveCommands()
	authentication := user.ReceiveAuthentication()
	connect := user.ReceiveConnections()
	disconnect := user.Connection.ReceiveDisconnect()

	toServer := user.Connection.ReceivePackets()
	toClient := user.ReceiveToMessages()

	// A context valid JUST for the lifetime of the user
	userCtx := user.Ctx()

	logger := user.Logger()

	go func() {
		err := RecordSession(
			userCtx,
			c.redis,
			c.settings.LogSessions,
			user,
		)
		if err != nil {
			logger.Warn().Err(err).Msg("failed to record client session")
		}
	}()

	defer user.Connection.Destroy()

	go c.PollFromMessages(ctx, user)
	go c.PollToMessages(ctx, user)

	for {
		logger = user.Logger()
		select {
		case <-ctx.Done():
			return

		// TODO
		//case event := <-names:
		//user := server.Users.FindUser(event.Client)

		//if user == nil {
		//continue
		//}

		//user.Mutex.Lock()
		//user.Name = event.Name
		//user.Mutex.Unlock()

		//logger := user.Logger()
		//logger.Info().Msg("client has new name")
		//server.NotifyNameChange(ctx, user, event.Name)

		case <-connect:
			user.Mutex.Lock()
			if user.Server != nil {
				instance := c.spaces.FindInstance(user.Server)
				if instance != nil {
					user.Space = instance
				}
			}
			user.Mutex.Unlock()

			logger := user.Logger()
			logger.Info().Msg("connected to server")

			isHome, err := user.IsAtHome(ctx)
			if err != nil {
				logger.Warn().Err(err).Msg("failed seeing if user was at home")
				continue
			}

			if isHome {
				space := user.GetSpace()
				message := fmt.Sprintf(
					"welcome to your home (space %s).",
					space.GetID(),
				)

				if user.IsLoggedIn() {
					user.Message(message)
					user.Message("editing by others is disabled. say #edit to enable it.")
				} else {
					user.Message(message + " anyone can edit it. because you are not logged in, it will be deleted in 4 hours")
				}
			}

		case authUser := <-authentication:
			if authUser == nil {
				c.GreetClient(userCtx, user)
				continue
			}

			logger = logger.With().Str("id", authUser.Discord.Id).Logger()

			user.Mutex.Lock()
			user.Auth = authUser
			user.Mutex.Unlock()

			verseUser, err := c.verse.GetOrCreateUser(userCtx, authUser)
			if err != nil {
				logger.Error().Err(err).Msg("failed to get verse state for user")
				continue
			}
			user.Verse = verseUser

			err = user.HydrateELOState(ctx, authUser)
			if err == nil {
				c.GreetClient(userCtx, user)
				continue
			}

			if err != redis.Nil {
				logger.Error().Err(err).Msg("failed to hydrate state for user")
				continue
			}

			// We save the initialized state that was there already
			err = user.SaveELOState(ctx)
			if err != nil {
				logger.Error().Err(err).Msg("failed to save elo state for user")
			}
			c.GreetClient(userCtx, user)
		case msg := <-toServer:
			data := msg.Data

			// We want to get the raw data from the user -- not the
			// deserialized messages
			user.RawFrom.Publish(io.RawPacket{
				Data:    data,
				Channel: msg.Channel,
			})

			messages, err := P.Decode(data, true)
			if err != nil {
				logger.Error().Err(err).
					Msg("client -> server (failed to decode message)")
				continue
			}

			server := user.GetServer()
			if server == nil {
				continue
			}

			processed := make([]P.Message, 0)
			for _, message := range messages {
				if !P.IsSpammyMessage(message.Type()) {
					logger.Debug().Str("code", message.Type().String()).Msg("client -> server")
				}

				data, err := user.From.Process(
					ctx,
					msg.Channel,
					message,
				)
				if err != nil {
					log.Error().Err(err).Msgf("failed to process message")
					continue
				}

				if data == nil {
					continue
				}

				newMessage, err := server.To.Process(
					ctx,
					msg.Channel,
					message,
				)
				if err != nil {
					log.Error().Err(err).Msgf("failed to process message for server")
					continue
				}

				processed = append(processed, newMessage)

			}

			server.Incoming() <- S.ServerPacket{
				Session:  uint32(user.Id),
				Channel:  msg.Channel,
				Messages: processed,
			}

		case msg := <-toClient:
			log.Info().Msgf("message to client")
			packet := msg.Packet
			done := msg.Done

			channel := uint8(packet.Channel)
			out := make([]byte, 0)
			filtered := make([]byte, 0)

			for _, message := range packet.Messages {
				type_ := message.Type()
				if !P.IsSpammyMessage(type_) {
					logger.Debug().
						Str("type", message.Type().String()).
						Msg("cluster -> client")
				}

				newMessage, err := user.To.Process(
					ctx,
					channel,
					message,
				)
				if err != nil {
					log.Error().Err(err).Msgf("failed to process message")
					continue
				}

				if newMessage == nil {
					continue
				}

				encoded, err := P.Encode(newMessage)
				if err != nil {
					log.Error().Err(err).Msgf("failed to encode message")
					continue
				}

				out = append(out, encoded...)

				if type_ == P.N_SENDDEMO || type_ == P.N_SENDMAP {
					continue
				}

				filtered = append(filtered, encoded...)
			}

			user.RawTo.Publish(io.RawPacket{
				Data:    filtered,
				Channel: channel,
			})

			ack := user.Connection.Send(io.RawPacket{
				Channel: uint8(packet.Channel),
				Data:    out,
			})

			go func() {
				select {
				case result := <-ack:
					done <- result
				case <-userCtx.Done():
					return
				}

			}()

		case request := <-commands:
			command := request.Command
			outChannel := request.Response

			go func() {
				handled, response, err := c.RunCommandWithTimeout(userCtx, command, user)
				outChannel <- ingress.CommandResult{
					Handled:  handled,
					Err:      err,
					Response: response,
				}
			}()
		case <-disconnect:
			c.NotifyClientChange(ctx, user, false)
			user.DisconnectFromServer()
		}
	}
}
