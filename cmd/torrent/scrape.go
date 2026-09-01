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
	var (
		cfg         scrapeCfg
		haveTracker bool
	)
	bargle.ParseAll(p,
		positional("tracker", &haveTracker, bargle.BuiltinUnmarshaler(&cfg.Tracker)),
		positionals("info-hashes", &cfg.InfoHashes, infoHashUnmarshaler),
	)
	requireArg(p, "tracker", haveTracker)
	requireArg(p, "info-hashes", len(cfg.InfoHashes) != 0)
	return func() error {
		return scrape(cfg)
	}
}

func infoHashUnmarshaler(target *infohash.T) bargle.Unmarshaler {
	return bargle.TextUnmarshaler(target)
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
