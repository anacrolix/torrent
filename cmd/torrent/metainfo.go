package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anacrolix/args"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/bradfitz/iter"
)

type pprintMetainfoFlags struct {
	JustName    bool
	PieceHashes bool
	Files       bool
}

func metainfoCmd(ctx args.SubCmdCtx) (err error) {
	var metainfoPath string
	var mi *metainfo.MetaInfo
	return ctx.NewParser().AddParams(
		args.Pos("torrent file", &metainfoPath, args.AfterParse(func() (err error) {
			mi, err = metainfo.LoadFromFile(metainfoPath)
			return
		})),
		args.Subcommand("magnet", func(ctx args.SubCmdCtx) (err error) {
			info, err := mi.UnmarshalInfo()
			if err != nil {
				return
			}
			fmt.Fprintf(os.Stdout, "%s\n", mi.Magnet(nil, &info).String())
			return nil
		}),
		args.Subcommand("pprint", func(ctx args.SubCmdCtx) (err error) {
			var flags pprintMetainfoFlags
			err = ctx.NewParser().AddParams(args.FromStruct(&flags)...).Parse()
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
		}),
		args.Subcommand("infohash", func(ctx args.SubCmdCtx) (err error) {
			fmt.Printf("%s: %s\n", mi.HashInfoBytes().HexString(), metainfoPath)
			return nil
		}),
		args.Subcommand("list-files", func(ctx args.SubCmdCtx) (err error) {
			info, err := mi.UnmarshalInfo()
			if err != nil {
				return fmt.Errorf("unmarshalling info from metainfo at %q: %v", metainfoPath, err)
			}
			for _, f := range info.UpvertedFiles() {
				fmt.Println(f.DisplayPath(&info))
			}
			return nil
		}),
	).Parse()
}

func pprintMetainfo(metainfo *metainfo.MetaInfo, flags pprintMetainfoFlags) error {
	info, err := metainfo.UnmarshalInfo()
	if err != nil {
		return fmt.Errorf("error unmarshalling info: %s", err)
	}
	if flags.JustName {
		fmt.Printf("%s\n", info.Name)
		return nil
	}
	d := map[string]interface{}{
		"Name":         info.Name,
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
