package main

import (
	"fmt"

	"github.com/anacrolix/tagflag"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/tracker"
)

func announceErr(args []string, parent *tagflag.Parser) error {
	var flags struct {
		tagflag.StartPos
		Tracker  string
		InfoHash torrent.InfoHash
	}
	tagflag.ParseArgs(&flags, args, tagflag.Parent(parent))
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
