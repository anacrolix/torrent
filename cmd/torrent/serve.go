package main

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/anacrolix/args"
	"github.com/anacrolix/log"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func serve(ctx args.SubCmdCtx) error {
	var filePath string
	ctx.Parse(args.Pos("filePath", &filePath))
	ctx.Defer(func() error {
		cfg := torrent.NewDefaultClientConfig()
		cfg.Seed = true
		cl, err := torrent.NewClient(cfg)
		if err != nil {
			return fmt.Errorf("new torrent client: %w", err)
		}
		defer cl.Close()
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			cl.WriteStatus(w)
		})
		info := metainfo.Info{
			PieceLength: 1 << 18,
		}
		err = info.BuildFromFilePath(filePath)
		if err != nil {
			return fmt.Errorf("building info from path %q: %w", filePath, err)
		}
		for _, fi := range info.Files {
			log.Printf("added %q", fi.Path)
		}
		mi := metainfo.MetaInfo{
			InfoBytes: bencode.MustMarshal(info),
		}
		pc, err := storage.NewDefaultPieceCompletionForDir(".")
		if err != nil {
			return fmt.Errorf("new piece completion: %w", err)
		}
		defer pc.Close()
		ih := mi.HashInfoBytes()
		to, _ := cl.AddTorrentOpt(torrent.AddTorrentOpts{
			InfoHash: ih,
			Storage: storage.NewFileOpts(storage.NewFileClientOpts{
				ClientBaseDir: filePath,
				FilePathMaker: func(opts storage.FilePathMakerOpts) string {
					return filepath.Join(opts.File.Path...)
				},
				TorrentDirMaker: nil,
				PieceCompletion: pc,
			}),
		})
		defer to.Drop()
		err = to.MergeSpec(&torrent.TorrentSpec{
			InfoBytes: mi.InfoBytes,
			Trackers: [][]string{{
				`wss://tracker.btorrent.xyz`,
				`wss://tracker.openwebtorrent.com`,
				"http://p4p.arenabg.com:1337/announce",
				"udp://tracker.opentrackr.org:1337/announce",
				"udp://tracker.openbittorrent.com:6969/announce",
			}},
		})
		if err != nil {
			return fmt.Errorf("setting trackers: %w", err)
		}
		fmt.Printf("%v: %v\n", to, to.Metainfo().Magnet(&ih, &info))
		select {}
	})
	return nil
}
