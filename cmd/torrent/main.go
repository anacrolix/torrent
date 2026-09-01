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
	_ "github.com/anacrolix/envpprof"
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
	defer p.DoHelpIfHelping()
	var debug bool
	bargle.ParseAll(p, desc("enable debug logging", boolFlag("debug", &debug)))
	if debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	run := parseSubcommand(p,
		subcommand{
			name:  "metainfo",
			desc:  "inspect a torrent file",
			parse: metainfoCmd,
		},
		subcommand{
			name:  "announce",
			desc:  "announce to a tracker",
			parse: announceCmd,
		},
		subcommand{
			name:  "scrape",
			desc:  "fetch swarm metrics for info-hashes from tracker",
			parse: scrapeCmd,
		},
		subcommand{
			name: "download",
			desc: "download torrents",
			parse: func(p *bargle.Parser) action {
				return downloadCmd(ctx, p, debug)
			},
		},
		subcommand{
			name:  "bencode",
			desc:  "reads bencoding from stdin into Go native types and spews the result",
			parse: bencodeCmd,
		},
		subcommand{
			name: "version",
			desc: "prints various protocol default version strings",
			parse: func(p *bargle.Parser) action {
				return func() error {
					fmt.Printf("HTTP User-Agent: %q\n", version.DefaultHttpUserAgent)
					fmt.Printf("Torrent client version: %q\n", version.DefaultExtendedHandshakeClientVersion)
					fmt.Printf("Torrent version prefix: %q\n", version.DefaultBep20Prefix)
					return nil
				}
			},
		},
		subcommand{
			name:  "serve",
			desc:  "creates and seeds a torrent from a filepath",
			parse: serveCmd,
		},
		subcommand{
			name:  "create",
			desc:  "creates a torrent metainfo for the file system rooted at ROOT, and outputs it to stdout",
			parse: createCmd,
		},
		subcommand{
			name:  "lpd",
			desc:  "Local Peer Discovery (BEP-14) tools — listen for or send LPD announcements without a full client",
			parse: lpdCmd,
		},
	)
	p.FailIfArgsRemain()
	if !p.Ok() || run == nil {
		return p.Err()
	}
	return run()
}

func bencodeCmd(p *bargle.Parser) action {
	return parseSubcommand(p,
		subcommand{
			name: "json",
			desc: "print the decoded values as indented JSON",
			parse: func(p *bargle.Parser) action {
				return func() error {
					je := json.NewEncoder(os.Stdout)
					je.SetIndent("", "  ")
					return decodeBencodeStdin(je.Encode)
				}
			},
		},
		subcommand{
			name: "spew",
			desc: "print the decoded values with go-spew",
			parse: func(p *bargle.Parser) action {
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
