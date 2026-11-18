package main

import (
	"context"
	"fmt"

	"github.com/anacrolix/bargle/v2"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/types/infohash"
)

type scrapeCfg struct {
	Tracker    string
	InfoHashes []infohash.T
}

func scrapeCmd(p *bargle.Parser) action {
	var cfg scrapeCfg
	trackerArg := bargle.Positional("tracker", bargle.BuiltinUnmarshaler(&cfg.Tracker))
	infoHashes := bargle.Positionals(
		"info-hashes",
		bargle.AppendSlice(&cfg.InfoHashes, infoHashUnmarshaler))
	bargle.ParseAll(p, trackerArg, infoHashes)
	p.Require(trackerArg)
	p.Require(infoHashes)
	return func() error {
		return scrape(cfg)
	}
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
