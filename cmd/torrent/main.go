// Downloads torrents from the command-line.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdLog "log"
	"log/slog"
	"net/http"
	"os"

	"github.com/anacrolix/bargle/v2"
	"github.com/anacrolix/envpprof"
	app "github.com/anacrolix/gostdapp"
	"github.com/anacrolix/log"
	expvar_prometheus "github.com/anacrolix/missinggo/v2/expvar-prometheus"

	"github.com/davecgh/go-spew/spew"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/version"
)

func init() {
	stdLog.SetFlags(stdLog.Flags() | stdLog.Lshortfile)
	prometheus.MustRegister(expvar_prometheus.NewCollector())
	http.Handle("/metrics", promhttp.Handler())
	log.Default = log.NewLogger().WithDefaultLevel(log.Info)
	log.Default.SetHandlers(log.SlogHandlerAsHandler{SlogHandler: slog.Default().Handler()})
}

func main() {
	app.RunContext(mainErr)
}

func mainErr(ctx context.Context) error {
	p := bargle.NewParser()
	var debug bool
	bargle.ParseAll(p, desc("enable debug logging", bargle.Flag("debug", &debug)))
	if debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	run := bargle.ParseSubcommand(p,
		subcommand{
			Name:  "metainfo",
			Desc:  "inspect a torrent file",
			Parse: metainfoCmd,
		},
		subcommand{
			Name:  "announce",
			Desc:  "announce to a tracker",
			Parse: announceCmd,
		},
		subcommand{
			Name:  "scrape",
			Desc:  "fetch swarm metrics for info-hashes from tracker",
			Parse: scrapeCmd,
		},
		subcommand{
			Name: "download",
			Desc: "download torrents",
			Parse: func(p *bargle.Parser) action {
				return downloadCmd(ctx, p, debug)
			},
		},
		subcommand{
			Name:  "bencode",
			Desc:  "reads bencoding from stdin into Go native types and spews the result",
			Parse: bencodeCmd,
		},
		subcommand{
			Name: "version",
			Desc: "prints various protocol default version strings",
			Parse: func(p *bargle.Parser) action {
				return func() error {
					fmt.Printf("HTTP User-Agent: %q\n", version.DefaultHttpUserAgent)
					fmt.Printf("Torrent client version: %q\n", version.DefaultExtendedHandshakeClientVersion)
					fmt.Printf("Torrent version prefix: %q\n", version.DefaultBep20Prefix)
					return nil
				}
			},
		},
		subcommand{
			Name:  "serve",
			Desc:  "creates and seeds a torrent from a filepath",
			Parse: serveCmd,
		},
		subcommand{
			Name:  "create",
			Desc:  "creates a torrent metainfo for the file system rooted at ROOT, and outputs it to stdout",
			Parse: createCmd,
		},
		subcommand{
			Name:  "lpd",
			Desc:  "Local Peer Discovery (BEP-14) tools — listen for or send LPD announcements without a full client",
			Parse: lpdCmd,
		},
	)
	p.FailIfArgsRemain()
	p.DoHelpIfHelping()
	if !p.Ok() || run == nil {
		// Getting the arguments wrong is the user's mistake, not a failure of the program. Report
		// it the way a command line is expected to, rather than returning it to be logged with a
		// timestamp, a level and a source location. Nothing has been started yet except what
		// envpprof does for itself, so there's nothing else to unwind.
		if err := p.Err(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			envpprof.Stop()
			os.Exit(2)
		}
		// Help was requested and printed.
		return nil
	}
	return run()
}

func bencodeCmd(p *bargle.Parser) action {
	return bargle.ParseSubcommand(p,
		subcommand{
			Name: "json",
			Desc: "print the decoded values as indented JSON",
			Parse: func(p *bargle.Parser) action {
				return func() error {
					je := json.NewEncoder(os.Stdout)
					je.SetIndent("", "  ")
					return decodeBencodeStdin(je.Encode)
				}
			},
		},
		subcommand{
			Name: "spew",
			Desc: "print the decoded values with go-spew",
			Parse: func(p *bargle.Parser) action {
				return func() error {
					config := spew.NewDefaultConfig()
					config.DisableCapacities = true
					config.Indent = "  "
					return decodeBencodeStdin(func(v any) error {
						config.Dump(v)
						return nil
					})
				}
			},
		},
	)
}

func decodeBencodeStdin(print func(any) error) error {
	d := bencode.NewDecoder(os.Stdin)
	for i := 0; ; i++ {
		var v any
		err := d.Decode(&v)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decoding message index %d: %w", i, err)
		}
		err = print(v)
		if err != nil {
			return fmt.Errorf("printing message index %d: %w", i, err)
		}
	}
}
