package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/anacrolix/torrent/tracker/udp"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
)

type ScrapeCmd struct {
	Tracker    string `arg:"positional"`
	InfoHashes []torrent.InfoHash
}

func scrape(flags ScrapeCmd) error {
	trackerUrl, err := url.Parse(flags.Tracker)
	if err != nil {
		return fmt.Errorf("parsing tracker url: %w", err)
	}
	cc, err := udp.NewConnClient(udp.NewConnClientOpts{
		Network: trackerUrl.Scheme,
		Host:    trackerUrl.Host,
		//Ipv6:    nil,
		//Logger:  log.Logger{},
	})
	if err != nil {
		return fmt.Errorf("creaing new udp tracker conn client: %w", err)
	}
	defer cc.Close()
	var ihs []udp.InfoHash
	for _, ih := range flags.InfoHashes {
		ihs = append(ihs, ih)
	}
	scrapeOut, err := cc.Client.Scrape(context.TODO(), ihs)
	if err != nil {
		return fmt.Errorf("scraping: %w", err)
	}
	spew.Dump(scrapeOut)
	return nil
}
