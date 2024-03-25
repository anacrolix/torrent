package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/anacrolix/bargle"
	"github.com/bradfitz/iter"

	"github.com/anacrolix/torrent/metainfo"
)

type pprintMetainfoFlags struct {
	JustName    bool
	PieceHashes bool
	Files       bool
}

func metainfoCmd() (cmd bargle.Command) {
	var metainfoPath string
	var mi *metainfo.MetaInfo
	// TODO: Test if bargle treats no subcommand as a failure.
	cmd.Positionals = append(cmd.Positionals,
		&bargle.Positional{
			Name:  "torrent file",
			Value: &bargle.String{Target: &metainfoPath},
			AfterParseFunc: func(ctx bargle.Context) error {
				ctx.AfterParse(func() (err error) {
					if strings.HasPrefix(metainfoPath, "http://") || strings.HasPrefix(metainfoPath, "https://") {
						response, err := http.Get(metainfoPath)
						if err != nil {
							return nil
						}
						mi, err = metainfo.Load(response.Body)
						if err != nil {
							return nil
						}
					} else {
						mi, err = metainfo.LoadFromFile(metainfoPath)
					}
					return
				})
				return nil
			},
		},
		bargle.Subcommand{Name: "magnet", Command: func() (cmd bargle.Command) {
			cmd.DefaultAction = func() (err error) {
				m, err := mi.MagnetV2()
				if err != nil {
					return
				}
				fmt.Fprintf(os.Stdout, "%v\n", m.String())
				return nil
			}
			return
		}()},
		bargle.Subcommand{Name: "pprint", Command: func() (cmd bargle.Command) {
			var flags pprintMetainfoFlags
			cmd = bargle.FromStruct(&flags)
			cmd.DefaultAction = func() (err error) {
				err = pprintMetainfo(mi, flags)
				if err != nil {
					return
				}
				if !flags.JustName {
					os.Stdout.WriteString("\n")
				}
				return
			}
			return
		}()},
		//bargle.Subcommand{Name: "infohash", Command: func(ctx args.SubCmdCtx) (err error) {
		//	fmt.Printf("%s: %s\n", mi.HashInfoBytes().HexString(), metainfoPath)
		//	return nil
		//}},
		//bargle.Subcommand{Name: "list-files", Command: func(ctx args.SubCmdCtx) (err error) {
		//	info, err := mi.UnmarshalInfo()
		//	if err != nil {
		//		return fmt.Errorf("unmarshalling info from metainfo at %q: %v", metainfoPath, err)
		//	}
		//	for _, f := range info.UpvertedFiles() {
		//		fmt.Println(f.DisplayPath(&info))
		//	}
		//	return nil
		//}},
	)
	return
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
