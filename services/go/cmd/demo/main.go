package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"io"
	"os"
	"time"

	"github.com/cfoust/sour/pkg/game"

	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog"
)

type DemoHeader struct {
	Magic    [16]byte
	Version  int32
	Protocol int32
}

type SectionHeader struct {
	Millis  int32
	Channel int32
	Length  int32
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	flag.Parse()
	args := flag.Args()

	if len(args) != 1 {
		log.Fatal().Msg("You must provide only a single argument.")
	}

	file, err := os.Open(args[0])

	if err != nil {
		log.Fatal().Err(err).Msg("could not open demo")
	}

	gz, err := gzip.NewReader(file)

	if err != nil {
		log.Fatal().Err(err).Msg("could not unzip demo")
	}

	defer file.Close()
	defer gz.Close()

	buffer, err := io.ReadAll(gz)
	reader := bytes.NewReader(buffer)

	header := DemoHeader{}
	err = binary.Read(reader, binary.LittleEndian, &header)

	section := SectionHeader{}
	for {
		err = binary.Read(reader, binary.LittleEndian, &section)

		bytes := make([]byte, section.Length)
		numRead, _ := reader.Read(bytes)
		if numRead == 0 {
			break
		}
		_, err = game.Read(bytes[:numRead])
		if err != nil {
			log.Error().Err(err).Msg("failed to parse messages")
			break
		}
	}
}
