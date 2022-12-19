package servers

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cfoust/sour/pkg/game"
	"github.com/cfoust/sour/pkg/game/messages"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type GameServer struct {
	Status ServerStatus
	Id     string
	// Another way for the client to refer to this server
	Alias      string
	NumClients int
	LastEvent  time.Time
	Mutex      sync.Mutex

	// The path of the socket
	path string
	// The working directory of the server
	wdir    string
	socket  *net.Conn
	command *exec.Cmd
	exit    chan bool
	send    chan []byte

	configFile  string
	description string

	rawBroadcasts chan game.GamePacket
	broadcasts    chan messages.Message
	subscribers   []chan messages.Message

	mapRequests chan MapRequest

	connects    chan uint32
	disconnects chan ForceDisconnect
	packets     chan ClientPacket
}

func (server *GameServer) ReceiveMapRequests() <-chan MapRequest {
	return server.mapRequests
}

func (server *GameServer) BroadcastSubscribe() <-chan messages.Message {
	server.Mutex.Lock()
	channel := make(chan messages.Message, 16)
	server.subscribers = append(server.subscribers, channel)
	server.Mutex.Unlock()
	return channel
}

func (server *GameServer) BroadcastUnsubscribe(channel <-chan messages.Message) {
	server.Mutex.Lock()
	newChannels := make([]chan messages.Message, 0)
	for _, subscriber := range server.subscribers {
		if subscriber == channel {
			continue
		}
		newChannels = append(newChannels, subscriber)
	}
	server.subscribers = newChannels
	server.Mutex.Unlock()
}

func (server *GameServer) sendMessage(data []byte) {
	p := game.Packet{}
	p.PutUint(uint32(len(data)))
	p = append(p, data...)
	server.send <- p
}

func (server *GameServer) SendData(clientId uint16, channel uint32, data []byte) {
	p := game.Packet{}
	p.PutUint(SOCKET_EVENT_RECEIVE)
	p.PutUint(uint32(clientId))
	p.PutUint(uint32(channel))
	p = append(p, data...)

	server.sendMessage(p)
}

func (server *GameServer) SendConnect(clientId uint16) {
	p := game.Packet{}
	p.PutUint(SOCKET_EVENT_CONNECT)
	p.PutUint(uint32(clientId))
	server.sendMessage(p)
}

func (server *GameServer) SendDisconnect(clientId uint16) {
	p := game.Packet{}
	p.PutUint(SOCKET_EVENT_DISCONNECT)
	p.PutUint(uint32(clientId))
	server.sendMessage(p)
}

func (server *GameServer) SendCommand(command string) {
	p := game.Packet{}
	p.PutUint(SOCKET_EVENT_COMMAND)
	p.PutString(command)
	server.sendMessage(p)
}

func (server *GameServer) SendPing() {
	p := game.Packet{}
	p.PutUint(SOCKET_EVENT_PING)
	server.sendMessage(p)
}

func (server *GameServer) SendMapResponse(mapName string, mode int32, succeeded int32) {
	p := game.Packet{}
	p.PutUint(SOCKET_EVENT_RESPOND_MAP)
	p.PutString(mapName)
	p.PutInt(mode)
	p.PutInt(succeeded)
	server.sendMessage(p)
}

func (server *GameServer) GetStatus() ServerStatus {
	server.Mutex.Lock()
	defer server.Mutex.Unlock()
	return server.Status
}

func (server *GameServer) SetStatus(status ServerStatus) {
	server.Mutex.Lock()
	server.Status = status
	server.Mutex.Unlock()
}

func (server *GameServer) IsRunning() bool {
	status := server.GetStatus()
	return status == ServerHealthy ||
		status == ServerStarting ||
		status == ServerStarted ||
		status == ServerLoadingMap
}

// Whether this string is a reference to this server (either an alias or an id).
func (server *GameServer) IsReference(reference string) bool {
	return server.Id == reference || server.Alias == reference
}

func (server *GameServer) Reference() string {
	if server.Alias != "" {
		return server.Alias
	}
	return server.Id
}

func Connect(path string) (*net.Conn, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}

	return &conn, nil
}

func (server *GameServer) Log() zerolog.Logger {
	return log.With().Str("server", server.Reference()).Logger()
}

func (server *GameServer) Shutdown() {
	status := server.GetStatus()

	if status != ServerHealthy {
		server.command.Process.Kill()
	}

	if server.socket != nil {
		(*server.socket).Close()
	}

	// Remove the socket if it's there
	if _, err := os.Stat(server.path); !os.IsNotExist(err) {
		os.Remove(server.path)
	}

	// And the config file
	if _, err := os.Stat(server.configFile); !os.IsNotExist(err) {
		os.Remove(server.configFile)
	}
}

func (server *GameServer) PollWrites(ctx context.Context) {
	for {
		select {
		case msg := <-server.send:
			if server.socket != nil {
				(*server.socket).Write(msg)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (server *GameServer) PollReads(ctx context.Context, out chan []byte) {
	buffer := make([]byte, 5242880)
	for {
		if ctx.Err() != nil {
			log.Error().Err(ctx.Err()).Msg("context error while polling")
			return
		}

		numBytes, err := (*server.socket).Read(buffer)
		if err != nil {
			continue
		}

		if numBytes == 0 {
			continue
		}

		result := make([]byte, numBytes)
		copy(result, buffer[:numBytes])
		out <- result
	}
}

func (server *GameServer) DecodeMessages(ctx context.Context) {
	logger := server.Log()
	for {
		select {
		case bundle := <-server.rawBroadcasts:
			// TODO handle files?
			if bundle.Channel == 2 {
				continue
			}

			decoded, err := messages.Read(bundle.Data)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to decode broadcast")
			}

			for _, message := range decoded {
				logger.Debug().Str("type", message.Type().String()).Msg("broadcast")
				server.broadcasts <- message
			}
		case <-ctx.Done():
			return
		}
	}
}

func (server *GameServer) PollEvents(ctx context.Context) {
	pingInterval := 500 * time.Millisecond
	pingTicker := time.NewTicker(pingInterval)
	lastPong := time.Now()
	server.SendPing()

	socketWrites := make(chan []byte, 16)

	go server.PollReads(ctx, socketWrites)
	go server.DecodeMessages(ctx)

	logger := log.With().Str("server", server.Reference()).Logger()

	for {
		select {
		case broadcast := <-server.broadcasts:
			server.Mutex.Lock()
			for _, subscriber := range server.subscribers {
				subscriber <- broadcast
			}
			server.Mutex.Unlock()
		case <-ctx.Done():
			return
		case <-pingTicker.C:
			if time.Now().Sub(lastPong) > 2*pingInterval {
				logger.Error().Msg("server stopped responding to pings, going down")
				server.Mutex.Lock()
				server.Status = ServerFailure
				server.Mutex.Unlock()
				return
			}
			server.SendPing()
		case msg := <-socketWrites:
			p := game.Packet(msg)

			for len(p) > 0 {
				type_, ok := p.GetUint()
				if !ok {
					logger.Debug().Uint32("type", type_).Msg("server -> cluster (invalid packet)")
					break
				}

				eventType := ServerEvent(type_)

				if eventType != SERVER_EVENT_PONG {
					logger.Debug().Str("type", eventType.String()).Msg("server -> cluster")
				}

				if eventType == SERVER_EVENT_REQUEST_MAP {
					mapName, ok := p.GetString()
					if !ok {
						break
					}

					mode, ok := p.GetInt()
					if !ok {
						break
					}

					server.mapRequests <- MapRequest{
						Map:  mapName,
						Mode: mode,
					}
					continue
				}

				if eventType == SERVER_EVENT_HEALTHY {
					server.SetStatus(ServerHealthy)
					continue
				}

				if eventType == SERVER_EVENT_PONG {
					lastPong = time.Now()
					continue
				}

				if eventType == SERVER_EVENT_CONNECT {
					id, ok := p.GetUint()
					if !ok {
						break
					}

					server.connects <- id
					continue
				}

				if eventType == SERVER_EVENT_DISCONNECT {
					id, ok := p.GetUint()
					if !ok {
						break
					}

					reason, ok := p.GetInt()
					if !ok {
						break
					}

					reasonText, ok := p.GetString()
					if !ok {
						break
					}

					server.disconnects <- ForceDisconnect{
						Client: id,
						Reason: reason,
						Text:   reasonText,
					}
					continue
				}

				numBytes, ok := p.GetUint()
				if !ok {
					break
				}

				if eventType == SERVER_EVENT_BROADCAST {
					chan_, ok := p.GetUint()
					if !ok {
						break
					}

					data := p[:numBytes]
					p = p[len(data):]

					server.rawBroadcasts <- game.GamePacket{
						Data:    data,
						Channel: uint8(chan_),
					}
					continue
				}

				id, ok := p.GetUint()
				if !ok {
					break
				}
				chan_, ok := p.GetUint()
				if !ok {
					break
				}

				data := p[:numBytes]
				p = p[len(data):]

				server.packets <- ClientPacket{
					Client: id,
					Packet: game.GamePacket{
						Data:    data,
						Channel: uint8(chan_),
					},
				}
			}
		}
	}
}

func (server *GameServer) Wait() {
	logger := server.Log()

	tailPipe := func(pipe io.ReadCloser, done chan bool) {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			logger.Info().Msg(scanner.Text())
		}
		done <- true
	}

	stdout, _ := server.command.StdoutPipe()
	stderr, _ := server.command.StderrPipe()

	err := server.command.Start()
	if err != nil {
		logger.Error().Err(err).Msg("failed to start server")
		return
	}

	logger.Info().Msg("server started")

	stdoutEOF := make(chan bool, 1)
	stderrEOF := make(chan bool, 1)

	go func(pipe io.ReadCloser, done chan bool) {
		scanner := bufio.NewScanner(pipe)

		for scanner.Scan() {
			message := scanner.Text()

			if strings.HasPrefix(message, "Join:") {
				server.Mutex.Lock()
				server.NumClients++
				server.LastEvent = time.Now()
				server.Mutex.Unlock()
			}

			if strings.HasPrefix(message, "Leave:") {
				server.Mutex.Lock()
				server.NumClients--
				server.LastEvent = time.Now()

				if server.NumClients < 0 {
					server.NumClients = 0
				}

				server.Mutex.Unlock()
			}

			logger.Info().Msg(message)
		}
		done <- true
	}(stdout, stdoutEOF)

	go tailPipe(stderr, stderrEOF)

	<-stdoutEOF
	<-stderrEOF

	state, err := server.command.Process.Wait()

	defer func() {
		server.exit <- true
	}()

	exitCode := state.ExitCode()
	if exitCode != 0 || err != nil {
		server.Mutex.Lock()
		server.Status = ServerFailure
		server.Mutex.Unlock()

		unixStatus := state.Sys().(syscall.WaitStatus)

		logger.Error().
			Err(err).
			Bool("continued", unixStatus.Continued()).
			Bool("coreDump", unixStatus.CoreDump()).
			Int("exitStatus", unixStatus.ExitStatus()).
			Bool("exited", unixStatus.Exited()).
			Bool("stopped", unixStatus.Stopped()).
			Str("stopSignal", unixStatus.StopSignal().String()).
			Str("signal", unixStatus.Signal().String()).
			Bool("signaled", unixStatus.Signaled()).
			Int("trapCause", unixStatus.TrapCause()).
			Msgf("[%s] exited with code %d", server.Reference(), exitCode)
		return
	}

	server.Mutex.Lock()
	server.Status = ServerExited
	server.Mutex.Unlock()

	logger.Info().Msg("exited")
}

func (server *GameServer) Start(ctx context.Context) error {
	logger := server.Log()
	tick := time.NewTicker(250 * time.Millisecond)

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Second*10)

	defer cancel()

	exitChannel := make(chan bool, 1)

	go server.Wait()

	for {
		status := server.GetStatus()

		// Check to see whether the socket is there
		if status == ServerStarting {
			conn, err := Connect(server.path)

			if err == nil {
				logger.Info().Msg("connected")
				server.Mutex.Lock()
				server.Status = ServerStarted
				server.socket = conn
				server.Mutex.Unlock()

				if len(server.description) > 0 {
					replaced := strings.Replace(server.description, "#id", server.Reference(), -1)
					go server.SendCommand(fmt.Sprintf("serverdesc \"%s\"", replaced))
				}
				go server.PollWrites(ctx)
				go server.PollEvents(ctx)

				exitChannel <- true
			}
		}

		select {
		case <-exitChannel:
			return nil
		case <-timeoutCtx.Done():
			return fmt.Errorf("starting server timed out")
		case <-tick.C:
			continue
		}
	}
}

func (server *GameServer) WaitUntilHealthy(ctx context.Context, timeout time.Duration) error {
	tick := time.NewTicker(100 * time.Millisecond)

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)

	defer cancel()

	for {
		status := server.GetStatus()
		if status == ServerHealthy {
			return nil
		}

		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("starting server timed out")
		case <-tick.C:
			continue
		}
	}
}

func (server *GameServer) StartAndWait(ctx context.Context) error {
	err := server.Start(ctx)
	if err != nil {
		return err
	}

	err = server.WaitUntilHealthy(ctx, 15*time.Second)
	if err != nil {
		return err
	}
	return nil
}
