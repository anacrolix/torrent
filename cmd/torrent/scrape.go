package main

import (
	"context"
	"fmt"

	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/tracker"
)

type scrapeCfg struct {
	Tracker    string             `arg:"positional"`
	InfoHashes []torrent.InfoHash `arity:"+" arg:"positional"`
}

func scrape(flags scrapeCfg) error {
	cc, err := tracker.NewClient(flags.Tracker, tracker.NewClientOpts{})
	if err != nil {
		err = fmt.Errorf("creating new tracker client: %w", err)
		return err
	}
	defer cc.Close()
	scrapeOut, err := cc.Scrape(context.TODO(), flags.InfoHashes)
	if err != nil {
		return fmt.Errorf("scraping: %w", err)
	}
	spew.Dump(scrapeOut)
	return nil
}
