package main

import (
	"fmt"

	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/tracker/udp"
)

type AnnounceCmd struct {
	Event    udp.AnnounceEvent
	Tracker  string           `arg:"positional"`
	InfoHash torrent.InfoHash `arg:"positional"`
}

func announceErr(flags AnnounceCmd) error {
	response, err := tracker.Announce{
		TrackerUrl: flags.Tracker,
		Request: tracker.AnnounceRequest{
			InfoHash: flags.InfoHash,
			Port:     uint16(torrent.NewDefaultClientConfig().ListenPort),
			NumWant:  -1,
			Event:    flags.Event,
		},
	}.Do()
	if err != nil {
		return fmt.Errorf("doing announce: %w", err)
	}
	spew.Dump(response)
	return nil
}
