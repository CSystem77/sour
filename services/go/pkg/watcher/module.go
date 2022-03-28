package watcher

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/cfoust/sour/pkg/enet"
)

/*
#cgo LDFLAGS: -lenet
#include <stdio.h>
#include <stdlib.h>
#include <enet/enet.h>

ENetAddress resolveServer(const char *host, int port) {
	ENetAddress serverAddress = { ENET_HOST_ANY, ENET_PORT_ANY };
	serverAddress.port = port;

	int result = enet_address_set_host(&serverAddress, host);
	if (result < 0) {
		serverAddress.host = ENET_HOST_ANY;
		serverAddress.port = ENET_PORT_ANY;
		return serverAddress;
	}

	return serverAddress;
}

ENetSocket initSocket() {
	ENetSocket sock = enet_socket_create(ENET_SOCKET_TYPE_DATAGRAM);
	enet_socket_set_option(sock, ENET_SOCKOPT_NONBLOCK, 1);
	enet_socket_set_option(sock, ENET_SOCKOPT_BROADCAST, 1);
	return sock;
}

void pingServer(ENetSocket socket, ENetAddress address) {
	ENetBuffer buf;
	char ping[10];
	ping[0] = 2;
	buf.data = ping;
	buf.dataLength = 10;
	enet_socket_send(socket, &address, &buf, 1);

}

int receiveServer(ENetSocket socket, ENetAddress address, void * output) {
	enet_uint32 events = ENET_SOCKET_WAIT_RECEIVE;
	ENetBuffer buf;
	buf.data = output;
	buf.dataLength = 128;
	while(enet_socket_wait(socket, &events, 0) >= 0 && events)
	{
		int len = enet_socket_receive(socket, &address, &buf, 1);
		return len;
	}
}

void destroySocket(ENetSocket sock) {
	enet_socket_destroy(sock);
}

*/
import "C"

// By default Sauerbraten seems to only allow one game server per IP address
// (or at least hostname) which is a little weird.
type Address struct {
	Host string
	Port int
}

type ServerInfo struct {
	name  string
	_map  string
	sdesc string
}

type Server struct {
	address *C.ENetAddress `cbor:"-"`
	socket  C.ENetSocket   `cbor:"-"`
	Info    []byte         `cbor:"info"`
}

type Servers map[Address]Server

type Watcher struct {
	serverMutex sync.Mutex
	servers     Servers
}

func NewWatcher() *Watcher {
	watcher := &Watcher{
		servers: make(Servers),
	}

	return watcher
}

func FetchServers() (Servers, error) {
	socket, err := enet.NewSocket("master.sauerbraten.org", 28787)
	defer socket.DestroySocket()
	if err != nil {
		fmt.Println("Error creating socket")
		return make(Servers), err
	}

	err = socket.SendString("list\n")
	if err != nil {
		fmt.Println("Error listing servers")
		return make(Servers), err
	}

	output, length := socket.Receive()
	if length < 0 {
		fmt.Println("Error receiving server list")
		return make(Servers), errors.New("Failed to receive server list")
	}

	// Collect the list of servers
	servers := make(Servers)
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "addserver") {
			continue
		}
		parts := strings.Split(line, " ")

		if len(parts) != 3 {
			continue
		}

		host := parts[1]
		port, err := strconv.Atoi(parts[2])

		if err != nil {
			continue
		}

		servers[Address{host, port}] = Server{
			address: nil,
			Info:    make([]byte, 256),
		}
	}

	// Resolve them to IPs
	for address, server := range servers {
		enetAddress := C.resolveServer(C.CString(address.Host), C.int(address.Port+1))
		if enetAddress.host == C.ENET_HOST_ANY {
			continue
		}

		server.address = &enetAddress
		server.socket = C.initSocket()
		servers[address] = server
	}

	return servers, nil
}

func (watcher *Watcher) UpdateServerList() {
	newServers, err := FetchServers()
	if err != nil {
		fmt.Println("Failed to fetch servers")
		return
	}

	watcher.serverMutex.Lock()
	oldServers := watcher.servers
	// We want to preserve the sockets from the old servers as
	// pings may have arrived as this operation happened
	for key, _ := range newServers {
		if oldServer, exists := oldServers[key]; exists {
			newServers[key] = oldServer
		}
	}

	// Also clean up old sockets if servers go away
	for key, oldServer := range oldServers {
		if _, exists := newServers[key]; !exists {
			C.destroySocket(oldServer.socket)
		}
	}
	watcher.servers = newServers
	watcher.serverMutex.Unlock()
}

func (watcher *Watcher) PingServers() {
	watcher.serverMutex.Lock()
	for _, server := range watcher.servers {
		address := server.address
		socket := server.socket
		if address == nil || socket == 0 {
			continue
		}
		C.pingServer(socket, *address)
	}
	watcher.serverMutex.Unlock()
}

func (watcher *Watcher) ReceivePings() {
	watcher.serverMutex.Lock()
	for key, server := range watcher.servers {
		address := server.address
		socket := server.socket
		if address == nil || socket == 0 {
			continue
		}
		result := make([]byte, 256)
		bytesRead := C.receiveServer(socket, *address, unsafe.Pointer(&result[0]))
		if bytesRead <= 0 {
			continue
		}
		server.Info = result
		watcher.servers[key] = server
	}
	watcher.serverMutex.Unlock()
}

func (watcher *Watcher) Get() Servers {
	watcher.serverMutex.Lock()
	servers := watcher.servers
	watcher.serverMutex.Unlock()
	return servers
}

func (watcher *Watcher) Watch() error {
	done := make(chan bool)

	go watcher.UpdateServerList()

	// We update the list of servers every minute
	serverListTicker := time.NewTicker(1 * time.Minute)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-serverListTicker.C:
				go watcher.UpdateServerList()

			}
		}
	}()

	// We send pings every 5 seconds, but don't block while waiting for results
	pingTicker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-pingTicker.C:
				go watcher.PingServers()

			}
		}
	}()

	// Every second we just check for any pings that came back
	receiveTicker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-receiveTicker.C:
				go watcher.ReceivePings()

			}
		}
	}()

	return nil
}
