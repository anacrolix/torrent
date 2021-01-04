package main

import (
	"fmt"

	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/tracker"
)

type AnnounceCmd struct {
	Tracker  string `arg:"positional"`
	InfoHash torrent.InfoHash
}

func announceErr() error {
	response, err := tracker.Announce{
		TrackerUrl: flags.Tracker,
		Request: tracker.AnnounceRequest{
			InfoHash: flags.InfoHash,
			Port:     uint16(torrent.NewDefaultClientConfig().ListenPort),
		},
	}.Do()
	if err != nil {
		return fmt.Errorf("doing announce: %w", err)
	}
	spew.Dump(response)
	return nil
}
