package main

import (
	"log"
	"os"

	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent/metainfo"
)

var (
	builtinAnnounceList = [][]string{
		{"udp://tracker.openbittorrent.com:80"},
		{"udp://tracker.publicbt.com:80"},
		{"udp://tracker.istole.it:6969"},
	}
)

func main() {
	log.SetFlags(log.Flags() | log.Lshortfile)
	var args struct {
		AnnonceList []string `short:"a" help:"set annonce server's"`
		tagflag.StartPos
		Root string `help:"Creates a torrent metainfo for the file system rooted at ROOT, and outputs it to stdout."`
	}
	tagflag.Parse(&args)
	mi := metainfo.MetaInfo{
		AnnounceList: builtinAnnounceList,
	}
	for _, a := range args.AnnonceList {
		mi.AnnounceList = append(mi.AnnounceList, []string{a})
	}
	mi.SetDefaults()
	err := mi.Info.BuildFromFilePath(args.Root)
	if err != nil {
		log.Fatal(err)
	}
	err = mi.Write(os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
}
