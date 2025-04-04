// Downloads torrents from the command-line.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdLog "log"
	"net/http"
	"os"
	"time"

	"github.com/anacrolix/bargle"
	"github.com/anacrolix/envpprof"
	app "github.com/anacrolix/gostdapp"
	"github.com/anacrolix/log"
	xprometheus "github.com/anacrolix/missinggo/v2/prometheus"
	"github.com/davecgh/go-spew/spew"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/version"
)

func init() {
	prometheus.MustRegister(xprometheus.NewExpvarCollector())
	http.Handle("/metrics", promhttp.Handler())
}

func shutdownTracerProvider(ctx context.Context, tp *trace.TracerProvider) {
	started := time.Now()
	err := tp.Shutdown(ctx)
	elapsed := time.Since(started)
	logger := log.Default.Slogger()
	logger.Debug("shutting down tracer provider", "took", elapsed)
	if err != nil && ctx.Err() == nil {
		log.Default.Slogger().Error("error shutting down tracer provider", "err", err)
	}
}

func main() {
	app.RunContext(mainErr)
}

func mainErr(ctx context.Context) error {
	defer stdLog.SetFlags(stdLog.Flags() | stdLog.Lshortfile)

	tracingExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return fmt.Errorf("creating tracing exporter: %w", err)
	}
	tracerProvider := trace.NewTracerProvider(trace.WithBatcher(tracingExporter))
	otel.SetTracerProvider(tracerProvider)

	main := bargle.Main{}
	main.Defer(envpprof.Stop)
	main.Defer(func() { shutdownTracerProvider(ctx, tracerProvider) })
	debug := false
	debugFlag := bargle.NewFlag(&debug)
	debugFlag.AddLong("debug")
	main.Options = append(main.Options, debugFlag.Make())
	main.Positionals = append(main.Positionals,
		bargle.Subcommand{Name: "metainfo", Command: metainfoCmd()},
		bargle.Subcommand{Name: "announce", Command: func() bargle.Command {
			var ac AnnounceCmd
			cmd := bargle.FromStruct(&ac)
			cmd.DefaultAction = func() error {
				return announceErr(ac)
			}
			return cmd
		}()},
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
				return downloadErr(ctx, downloadFlags{
					Debug:       debug,
					DownloadCmd: dlc,
				})
			}
			return cmd
		}()},
		bargle.Subcommand{
			Name: "bencode",
			Command: func() (cmd bargle.Command) {
				var print func(interface{}) error
				cmd.Positionals = append(cmd.Positionals,
					bargle.Subcommand{Name: "json", Command: func() (cmd bargle.Command) {
						cmd.DefaultAction = func() error {
							je := json.NewEncoder(os.Stdout)
							je.SetIndent("", "  ")
							print = je.Encode
							return nil
						}
						return
					}()},
					bargle.Subcommand{Name: "spew", Command: func() (cmd bargle.Command) {
						cmd.DefaultAction = func() error {
							config := spew.NewDefaultConfig()
							config.DisableCapacities = true
							config.Indent = "  "
							print = func(v interface{}) error {
								config.Dump(v)
								return nil
							}
							return nil
						}
						return
					}()})
				d := bencode.NewDecoder(os.Stdin)
				cmd.AfterParseFunc = func(ctx bargle.Context) error {
					ctx.AfterParse(func() error {
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
				}
				cmd.Desc = "reads bencoding from stdin into Go native types and spews the result"
				return
			}(),
		},
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
	// Well this sux, this old version of bargle doesn't return so we can let the gostdapp Context
	// clean up.
	main.Run()
	return nil
}
