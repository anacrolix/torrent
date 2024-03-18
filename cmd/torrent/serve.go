package main

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/anacrolix/bargle"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func serve() (cmd bargle.Command) {
	var filePaths []string
	cmd.Positionals = append(cmd.Positionals, &bargle.Positional{
		Value: bargle.AutoUnmarshaler(&filePaths),
	})
	cmd.Desc = "creates and seeds a torrent from a filepath"
	cmd.DefaultAction = func() error {
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
		for _, filePath := range filePaths {
			totalLength, err := totalLength(filePath)
			if err != nil {
				return fmt.Errorf("calculating total length of %q: %v", filePath, err)
			}
			pieceLength := metainfo.ChoosePieceLength(totalLength)
			info := metainfo.Info{
				PieceLength: pieceLength,
			}
			err = info.BuildFromFilePath(filePath)
			if err != nil {
				return fmt.Errorf("building info from path %q: %w", filePath, err)
			}
			for _, fi := range info.UpvertedFiles() {
				log.Printf("added %q", fi.BestPath())
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
						return filepath.Join(opts.File.BestPath()...)
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
		}
		select {}
	}
	return
}
