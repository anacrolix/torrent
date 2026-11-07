package main

import (
	"fmt"

	"github.com/anacrolix/bargle/v2"
	g "github.com/anacrolix/generics"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/tracker/udp"
	"github.com/anacrolix/torrent/types/infohash"
)

type AnnounceCmd struct {
	Event    udp.AnnounceEvent
	Port     g.Option[uint16]
	Tracker  string
	InfoHash infohash.T
}

func announceCmd(p *bargle.Parser) action {
	var args AnnounceCmd
	trackerArg := bargle.Positional("tracker", bargle.BuiltinUnmarshaler(&args.Tracker))
	infoHashArg := bargle.Positional("info-hash", bargle.TextUnmarshaler(&args.InfoHash))
	bargle.ParseAll(p,
		desc("announce event (completed, started, or stopped)",
			bargle.Long("event", bargle.TextUnmarshaler(&args.Event))),
		desc("port to announce, defaults to the client's listen port",
			bargle.Long("port", bargle.BuiltinOptionUnmarshaler(&args.Port))),
		trackerArg,
		infoHashArg,
	)
	p.Require(trackerArg)
	p.Require(infoHashArg)
	return func() error {
		return announceErr(args)
	}
}

func announceErr(flags AnnounceCmd) error {
	req := tracker.AnnounceRequest{
		InfoHash: flags.InfoHash,
		Port:     uint16(torrent.NewDefaultClientConfig().ListenPort),
		NumWant:  -1,
		Event:    flags.Event,
		Left:     -1,
	}
	if flags.Port.Ok {
		req.Port = flags.Port.Value
	}
	response, err := tracker.Announce{
		TrackerUrl: flags.Tracker,
		Request:    req,
	}.Do()
	if err != nil {
		return fmt.Errorf("doing announce: %w", err)
	}
	spew.Dump(response)
	return nil
}
