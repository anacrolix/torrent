package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/anacrolix/torrent/bencode"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/exts/getput"
	"github.com/anacrolix/dht/v2/krpc"
)

type GetCmd struct {
	Target          []krpc.ID `arg:"positional"`
	Seq             *int64
	Salt            string
	ExtractInfohash bool `help:"extract and print the mutable torrent infohash in hex"`
}

func get(cmd *GetCmd) (err error) {
	s, err := dht.NewServer(nil)
	if err != nil {
		return
	}
	defer s.Close()
	if len(cmd.Target) == 0 {
		return errors.New("no targets specified")
	}
	for _, t := range cmd.Target {
		log.Printf("getting %v", t)
		v, _, err := getput.Get(context.Background(), t, s, cmd.Seq, []byte(cmd.Salt))
		if err != nil {
			log.Printf("error getting %v: %v", t, err)
			continue
		}
		log.Printf("got result [seq=%v, mutable=%v]", v.Seq, v.Mutable)
		if cmd.ExtractInfohash {
			var payload krpc.Bep46Payload
			err = bencode.Unmarshal(v.V, &payload)
			if err != nil {
				return fmt.Errorf("unmarshalling bep46 payload: %w", err)
			}
			fmt.Printf("%x\n", payload.Ih)
		} else {
			os.Stdout.Write(bencode.MustMarshal(v.V))
			os.Stdout.WriteString("\n")
		}
	}
	return nil
}
