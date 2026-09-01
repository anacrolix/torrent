package main

import (
	"fmt"

	"github.com/anacrolix/bargle/v2"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/tracker/udp"
	"github.com/anacrolix/torrent/types/infohash"
)

type AnnounceCmd struct {
	Event    udp.AnnounceEvent
	Port     *uint16
	Tracker  string
	InfoHash infohash.T
}

func announceCmd(p *bargle.Parser) action {
	var (
		args                      AnnounceCmd
		haveTracker, haveInfoHash bool
	)
	bargle.ParseAll(p,
		desc("announce event (completed, started, or stopped)", textLong("event", &args.Event)),
		desc("port to announce, defaults to the client's listen port",
			bargle.Long("port", pointerUnmarshaler(&args.Port, uint16Unmarshaler))),
		positional("tracker", &haveTracker, bargle.BuiltinUnmarshaler(&args.Tracker)),
		positional("info-hash", &haveInfoHash, bargle.TextUnmarshaler(&args.InfoHash)),
	)
	requireArg(p, "tracker", haveTracker)
	requireArg(p, "info-hash", haveInfoHash)
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
	if flags.Port != nil {
		req.Port = *flags.Port
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
