package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/anacrolix/bargle/v2"
	"github.com/bradfitz/iter"

	"github.com/anacrolix/torrent/metainfo"
)

type pprintMetainfoFlags struct {
	JustName    bool
	PieceHashes bool
	Files       bool
}

func metainfoCmd(p *bargle.Parser) action {
	var (
		metainfoPath string
		havePath     bool
	)
	bargle.ParseAll(p, positional("torrent file", &havePath, bargle.BuiltinUnmarshaler(&metainfoPath)))
	requireArg(p, "torrent file", havePath)
	return parseSubcommand(p,
		subcommand{
			name: "magnet",
			desc: "print a v2 magnet link for the torrent",
			parse: func(p *bargle.Parser) action {
				return func() error {
					mi, err := loadMetainfo(metainfoPath)
					if err != nil {
						return err
					}
					m, err := mi.MagnetV2()
					if err != nil {
						return err
					}
					fmt.Fprintf(os.Stdout, "%v\n", m.String())
					return nil
				}
			},
		},
		subcommand{
			name: "pprint",
			desc: "pretty print the torrent's metainfo as JSON",
			parse: func(p *bargle.Parser) action {
				var flags pprintMetainfoFlags
				bargle.ParseAll(p,
					desc("only print the torrent name", boolFlag("just-name", &flags.JustName)),
					desc("include piece hashes", boolFlag("piece-hashes", &flags.PieceHashes)),
					desc("include files", boolFlag("files", &flags.Files)),
				)
				return func() (err error) {
					mi, err := loadMetainfo(metainfoPath)
					if err != nil {
						return
					}
					err = pprintMetainfo(mi, flags)
					if err != nil {
						return
					}
					if !flags.JustName {
						os.Stdout.WriteString("\n")
					}
					return
				}
			},
		},
	)
}

// Loads a metainfo from a local file path, or an HTTP(S) URL.
func loadMetainfo(path string) (_ *metainfo.MetaInfo, err error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		response, err := http.Get(path)
		if err != nil {
			return nil, fmt.Errorf("getting %q: %w", path, err)
		}
		defer response.Body.Close()
		mi, err := metainfo.Load(response.Body)
		if err != nil {
			return nil, fmt.Errorf("loading metainfo from %q: %w", path, err)
		}
		return mi, nil
	}
	mi, err := metainfo.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading metainfo from file %q: %w", path, err)
	}
	return mi, nil
}

func pprintMetainfo(metainfo *metainfo.MetaInfo, flags pprintMetainfoFlags) error {
	info, err := metainfo.UnmarshalInfo()
	if err != nil {
		return fmt.Errorf("error unmarshalling info: %s", err)
	}
	if flags.JustName {
		fmt.Printf("%s\n", info.BestName())
		return nil
	}
	d := map[string]interface{}{
		"Name":         info.Name,
		"Name.Utf8":    info.NameUtf8,
		"NumPieces":    info.NumPieces(),
		"PieceLength":  info.PieceLength,
		"InfoHash":     metainfo.HashInfoBytes().HexString(),
		"NumFiles":     len(info.UpvertedFiles()),
		"TotalLength":  info.TotalLength(),
		"Announce":     metainfo.Announce,
		"AnnounceList": metainfo.AnnounceList,
		"UrlList":      metainfo.UrlList,
	}
	if len(metainfo.Nodes) > 0 {
		d["Nodes"] = metainfo.Nodes
	}
	if flags.Files {
		d["Files"] = info.UpvertedFiles()
	}
	if flags.PieceHashes {
		d["PieceHashes"] = func() (ret []string) {
			for i := range iter.N(info.NumPieces()) {
				ret = append(ret, hex.EncodeToString(info.Pieces[i*20:(i+1)*20]))
			}
			return
		}()
	}
	b, _ := json.MarshalIndent(d, "", "  ")
	_, err = os.Stdout.Write(b)
	return err
}
