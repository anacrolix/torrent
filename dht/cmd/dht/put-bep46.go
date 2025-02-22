package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/anacrolix/args/targets"
	"github.com/anacrolix/torrent/types/infohash"
	"github.com/multiformats/go-base36"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/bep44"
	"github.com/anacrolix/dht/v2/exts/getput"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/dht/v2/traversal"
)

type PutMutableInfohash struct {
	Key     targets.Hex
	Seq     int64
	Cas     int64
	Salt    string
	AutoSeq bool
}

func putMutableInfohash(cmd *PutMutableInfohash, ih infohash.T) (err error) {
	s, err := dht.NewServer(nil)
	if err != nil {
		return
	}
	defer s.Close()
	put := bep44.Put{
		V:    krpc.Bep46Payload{Ih: ih},
		Salt: []byte(cmd.Salt),
		Cas:  cmd.Cas,
		Seq:  cmd.Seq,
	}
	seed := cmd.Key.Bytes
	if len(seed) != ed25519.SeedSize {
		err = fmt.Errorf("seed size %v: expected %v", len(seed), ed25519.SeedSize)
		return
	}
	privKey := ed25519.NewKeyFromSeed(seed)
	put.K = (*[32]byte)(privKey.Public().(ed25519.PublicKey))
	target := put.Target()
	log.Printf("putting %q to %x", put.V, target)
	var stats *traversal.Stats
	stats, err = getput.Put(
		context.Background(),
		target,
		s,
		put.Salt,
		makeSeqToPut(cmd.AutoSeq, true, put, privKey),
	)
	if err != nil {
		err = fmt.Errorf("in traversal: %w", err)
		return
	}
	log.Printf("%+v", stats)
	fmt.Printf("%x\n", target)
	link := fmt.Sprintf("magnet:?xs=urn:btpk:%x", *put.K)
	if len(put.Salt) != 0 {
		link += "&s=" + hex.EncodeToString(put.Salt)
	}
	fmt.Println(link)
	fmt.Println("base32hex", base32.HexEncoding.EncodeToString(put.K[:]))
	fmt.Println("base36lc", base36.EncodeToStringLc(put.K[:]))
	return nil
}
