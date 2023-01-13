//#include "game.h"
#ifndef WIN32
#include <sys/socket.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <unistd.h>
#include <stdio.h>
#include <sys/un.h>
#include <netinet/in.h>
#include <fcntl.h>
#endif
#include "socket.h"
#include "../mod/QServ.h"

SVAR(socketpath, "/tmp/qserv_sock");

SocketChannel socketCtl;

int SocketChannel::getSock()
{
    return sockFd;
}

bool SocketChannel::isConnected() {
	return connected;
}

void SocketChannel::checkConnection() {
    if (connected) return;
    int result = accept(sockFd, NULL, NULL);
    if (result == -1) {
        return;
    }

    clientFd = result;
    connected = true;

    if (preconnectOffset > 0) {
        send(preconnect, preconnectOffset);
    }
}

int SocketChannel::send(char * data, int length) {
    if (!connected) {
        for (int i = 0; i < length; i++) {
            preconnect[preconnectOffset++] = data[i];
        }
        return length;
    }
    return write(clientFd, data, length);
}

void SocketChannel::init()
{
    struct sockaddr_un sa;
    struct hostent *he;

    sockFd = socket(AF_UNIX, SOCK_STREAM, 0);

    memset(&sa, 0, sizeof(struct sockaddr_un));
    sa.sun_family = AF_UNIX;
    strncpy(sa.sun_path, socketpath, sizeof(sa.sun_path) - 1);

    int result = bind(sockFd, (struct sockaddr *) &sa, sizeof(struct sockaddr_un));
    if (result == -1) {
        printf("Failed to bind to socket %s\n", socketpath);
        return;
    }

    result = listen(sockFd, 5);
    if (result == -1) {
        printf("Failed to listen on socket %s\n", socketpath);
        return;
    }
}

int SocketChannel::receive(ENetPacket * packet)
{
    checkConnection();
    if (!connected) return -1;

    ssize_t numBytes = read(clientFd, buffer, sizeof(buffer));
    if (numBytes <= 0) {
        if (errno == ECONNRESET ||
            errno == ENOTCONN ||
            errno == ETIMEDOUT) {
            connected = false;
        }
        return -1;
    }
    packet->data = buffer;
    packet->dataLength = numBytes;
    return 0;
}

void SocketChannel::finish()
{
    close(sockFd);
}
