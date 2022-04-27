// Downloads torrents from the command-line.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdLog "log"
	"net/http"
	"os"

	"github.com/anacrolix/args"
	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	xprometheus "github.com/anacrolix/missinggo/v2/prometheus"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/version"
	"github.com/davecgh/go-spew/spew"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	if err := mainErr(); err != nil {
		log.Printf("error in main: %v", err)
		os.Exit(1)
	}
}

func init() {
	prometheus.MustRegister(xprometheus.NewExpvarCollector())
	http.Handle("/metrics", promhttp.Handler())
}

func mainErr() error {
	defer envpprof.Stop()
	stdLog.SetFlags(stdLog.Flags() | stdLog.Lshortfile)
	debug := args.Flag(args.FlagOpt{Long: "debug"})
	args.ParseMain(
		debug,
		args.Subcommand("metainfo", metainfoCmd),
		args.Subcommand("announce", func(p args.SubCmdCtx) error {
			var cmd AnnounceCmd
			err := p.NewParser().AddParams(
				args.Pos("tracker", &cmd.Tracker),
				args.Pos("infohash", &cmd.InfoHash)).Parse()
			if err != nil {
				return err
			}
			return announceErr(cmd)
		}),
		args.Subcommand("scrape", func(p args.SubCmdCtx) error {
			var cmd ScrapeCmd
			err := p.NewParser().AddParams(
				args.Pos("tracker", &cmd.Tracker),
				args.Pos("infohash", &cmd.InfoHashes, args.Arity('+'))).Parse()
			if err != nil {
				return err
			}
			return scrape(cmd)
		}),
		args.Subcommand("download", func(p args.SubCmdCtx) error {
			var dlc DownloadCmd
			err := p.NewParser().AddParams(
				append(args.FromStruct(&dlc), debug)...,
			).Parse()
			if err != nil {
				return err
			}
			dlf := downloadFlags{
				Debug:       debug.Bool(),
				DownloadCmd: dlc,
			}
			p.Defer(func() error {
				return downloadErr(dlf)
			})
			return nil
		}),
		args.Subcommand(
			"bencode",
			func(p args.SubCmdCtx) error {
				var print func(interface{}) error
				if !p.Parse(
					args.Subcommand("json", func(ctx args.SubCmdCtx) (err error) {
						ctx.Parse()
						je := json.NewEncoder(os.Stdout)
						je.SetIndent("", "  ")
						print = je.Encode
						return nil
					}),
					args.Subcommand("spew", func(ctx args.SubCmdCtx) (err error) {
						ctx.Parse()
						config := spew.NewDefaultConfig()
						config.DisableCapacities = true
						config.Indent = "  "
						print = func(v interface{}) error {
							config.Dump(v)
							return nil
						}
						return nil
					}),
				).RanSubCmd {
					return errors.New("an output type is required")
				}
				d := bencode.NewDecoder(os.Stdin)
				p.Defer(func() error {
					for i := 0; ; i++ {
						var v interface{}
						err := d.Decode(&v)
						if err == io.EOF {
							break
						}
						if err != nil {
							return fmt.Errorf("decoding message index %d: %w", i, err)
						}
						print(v)
					}
					return nil
				})
				return nil
			},
			args.Help("reads bencoding from stdin into Go native types and spews the result"),
		),
		args.Subcommand("version", func(p args.SubCmdCtx) error {
			fmt.Printf("HTTP User-Agent: %q\n", version.DefaultHttpUserAgent)
			fmt.Printf("Torrent client version: %q\n", version.DefaultExtendedHandshakeClientVersion)
			fmt.Printf("Torrent version prefix: %q\n", version.DefaultBep20Prefix)
			return nil
		}),
		args.Subcommand("serve", serve, args.Help("creates and seeds a torrent from a filepath")),
	)
	return nil
}
