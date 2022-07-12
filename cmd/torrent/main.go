// Downloads torrents from the command-line.
package main

import (
	"fmt"
	stdLog "log"
	"net/http"

	"github.com/anacrolix/bargle"
	"github.com/anacrolix/envpprof"
	xprometheus "github.com/anacrolix/missinggo/v2/prometheus"
	"github.com/anacrolix/torrent/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func init() {
	prometheus.MustRegister(xprometheus.NewExpvarCollector())
	http.Handle("/metrics", promhttp.Handler())
}

func main() {
	defer stdLog.SetFlags(stdLog.Flags() | stdLog.Lshortfile)
	main := bargle.Main{}
	main.Defer(envpprof.Stop)
	debug := false
	debugFlag := bargle.NewFlag(&debug)
	debugFlag.AddLong("debug")
	main.Options = append(main.Options, debugFlag.Make())
	main.Positionals = append(main.Positionals,
		bargle.Subcommand{Name: "metainfo", Command: metainfoCmd()},
		//bargle.Subcommand{Name: "announce", Command: func() bargle.Command {
		//	var cmd AnnounceCmd
		//	err := p.NewParser().AddParams(
		//		args.Pos("tracker", &cmd.Tracker),
		//		args.Pos("infohash", &cmd.InfoHash)).Parse()
		//	if err != nil {
		//		return err
		//	}
		//	return announceErr(cmd)
		//}()},
		bargle.Subcommand{Name: "scrape", Command: func() bargle.Command {
			var scrapeCfg scrapeCfg
			cmd := bargle.FromStruct(&scrapeCfg)
			cmd.Desc = "fetch swarm metrics for info-hashes from tracker"
			cmd.DefaultAction = func() error {
				return scrape(scrapeCfg)
			}
			return cmd
		}()},
		bargle.Subcommand{Name: "download", Command: func() bargle.Command {
			var dlc DownloadCmd
			cmd := bargle.FromStruct(&dlc)
			cmd.DefaultAction = func() error {
				return downloadErr(downloadFlags{
					Debug:       debug,
					DownloadCmd: dlc,
				})
			}
			return cmd
		}()},
		//bargle.Subcommand{Name:
		//	"bencode", Command: func() bargle.Command {
		//		var print func(interface{}) error
		//		if !p.Parse(
		//			args.Subcommand("json", func(ctx args.SubCmdCtx) (err error) {
		//				ctx.Parse()
		//				je := json.NewEncoder(os.Stdout)
		//				je.SetIndent("", "  ")
		//				print = je.Encode
		//				return nil
		//			}),
		//			args.Subcommand("spew", func(ctx args.SubCmdCtx) (err error) {
		//				ctx.Parse()
		//				config := spew.NewDefaultConfig()
		//				config.DisableCapacities = true
		//				config.Indent = "  "
		//				print = func(v interface{}) error {
		//					config.Dump(v)
		//					return nil
		//				}
		//				return nil
		//			}),
		//		).RanSubCmd {
		//			return errors.New("an output type is required")
		//		}
		//		d := bencode.NewDecoder(os.Stdin)
		//		p.Defer(func() error {
		//			for i := 0; ; i++ {
		//				var v interface{}
		//				err := d.Decode(&v)
		//				if err == io.EOF {
		//					break
		//				}
		//				if err != nil {
		//					return fmt.Errorf("decoding message index %d: %w", i, err)
		//				}
		//				print(v)
		//			}
		//			return nil
		//		})
		//		return nil
		//	}(),
		//	Desc: "reads bencoding from stdin into Go native types and spews the result",
		//},
		bargle.Subcommand{Name: "version", Command: bargle.Command{
			DefaultAction: func() error {
				fmt.Printf("HTTP User-Agent: %q\n", version.DefaultHttpUserAgent)
				fmt.Printf("Torrent client version: %q\n", version.DefaultExtendedHandshakeClientVersion)
				fmt.Printf("Torrent version prefix: %q\n", version.DefaultBep20Prefix)
				return nil
			},
			Desc: "prints various protocol default version strings",
		}},
		bargle.Subcommand{Name: "serve", Command: serve()},
		bargle.Subcommand{Name: "create", Command: create()},
	)
	main.Run()
}
