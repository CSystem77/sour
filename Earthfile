cpp:
    FROM ubuntu:20.04
    ## for apt to be noninteractive
    ENV DEBIAN_FRONTEND noninteractive
    ENV DEBCONF_NONINTERACTIVE_SEEN true
    WORKDIR /code
    RUN apt-get update && apt-get install -y build-essential cmake zlib1g-dev inotify-tools
    SAVE IMAGE sour:cpp

proxy:
    FROM +cpp
    COPY services/proxy .
    RUN ./build
    SAVE ARTIFACT wsproxy AS LOCAL "earthly/wsproxy"

go:
    FROM golang:1.18
    RUN apt-get update && apt-get install -qqy libenet-dev inotify-tools build-essential cmake zlib1g-dev inotify-tools
    SAVE IMAGE sour:go

goexe:
    FROM +go
    COPY services/go .
    RUN ./build
    SAVE ARTIFACT cluster AS LOCAL "earthly/cluster"
    SAVE ARTIFACT mapdump AS LOCAL "earthly/mapdump"

emscripten:
    FROM emscripten/emsdk:3.1.8
    RUN apt-get update && apt-get install -y ucommon-utils inotify-tools imagemagick zlib1g-dev unrar
    SAVE IMAGE sour:emscripten

assets:
    ARG hash
    FROM +emscripten
    WORKDIR /tmp
    COPY services/assets assets
    COPY +goexe/mapdump assets/mapdump
    RUN --mount=type=cache,target=/tmp/assets/working /tmp/assets/build
    SAVE ARTIFACT assets/output AS LOCAL "earthly/assets"

game:
    FROM +emscripten
    WORKDIR /cube2
    COPY services/game /game
    RUN --mount=type=cache,target=/emsdk/upstream/emscripten/cache/ /game/build
    SAVE ARTIFACT /game/dist/game AS LOCAL "earthly/game"

client:
    FROM node:14.17.5
    WORKDIR /client
    COPY services/client .
    RUN --mount=type=cache,target=/code/node_modules yarn install
    RUN rm -rf dist && yarn build && cp src/index.html src/favicon.ico dist
    SAVE ARTIFACT dist AS LOCAL "earthly/client"

image-slim:
  FROM golang:1.18
  RUN go install cuelang.org/go/cmd/cue@latest
  RUN apt-get update && apt-get install -y nginx libenet-dev
  COPY config/schema.cue /sour/schema.cue
  COPY config/default-config.yaml /sour/default-config.yaml
  COPY +goexe/cluster /bin/cluster
  COPY +proxy/wsproxy /bin/wsproxy
  COPY +game/game /app/game/
  COPY +client/dist /app/
  COPY services/ingress/production.conf /etc/nginx/conf.d/default.conf
  COPY entrypoint /bin/entrypoint
  CMD ["/bin/entrypoint"]
  SAVE IMAGE sour:slim

image:
  FROM +image-slim
  COPY +assets/output /app/assets/
  SAVE IMAGE sour:latest

github:
  FROM +image
  ARG tag
  SAVE IMAGE --push ghcr.io/cfoust/sour:$tag

push-slim:
  FROM +image-slim
  SAVE IMAGE --push registry.digitalocean.com/cfoust/sour:latest

push:
  FROM +image
  SAVE IMAGE --push registry.digitalocean.com/cfoust/sour:latest
