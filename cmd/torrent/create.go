package main

import (
	"os"

	"github.com/anacrolix/bargle"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

var builtinAnnounceList = [][]string{
	{"http://p4p.arenabg.com:1337/announce"},
	{"udp://tracker.opentrackr.org:1337/announce"},
	{"udp://tracker.openbittorrent.com:6969/announce"},
}

func create() (cmd bargle.Command) {
	var args struct {
		AnnounceList      []string `name:"a" help:"extra announce-list tier entry"`
		EmptyAnnounceList bool     `name:"n" help:"exclude default announce-list entries"`
		Comment           string   `name:"t" help:"comment"`
		CreatedBy         string   `name:"c" help:"created by"`
		InfoName          *string  `name:"i" help:"override info name (defaults to ROOT)"`
		PieceLength       tagflag.Bytes
		Url               []string `name:"u" help:"add webseed url"`
		Private           *bool
		Root              string `arg:"positional"`
	}
	cmd = bargle.FromStruct(&args)
	cmd.Desc = "Creates a torrent metainfo for the file system rooted at ROOT, and outputs it to stdout"
	cmd.DefaultAction = func() (err error) {
		mi := metainfo.MetaInfo{
			AnnounceList: builtinAnnounceList,
		}
		if args.EmptyAnnounceList {
			mi.AnnounceList = make([][]string, 0)
		}
		for _, a := range args.AnnounceList {
			mi.AnnounceList = append(mi.AnnounceList, []string{a})
		}
		mi.SetDefaults()
		if len(args.Comment) > 0 {
			mi.Comment = args.Comment
		}
		if len(args.CreatedBy) > 0 {
			mi.CreatedBy = args.CreatedBy
		}
		mi.UrlList = args.Url
		info := metainfo.Info{
			PieceLength: args.PieceLength.Int64(),
			Private:     args.Private,
		}
		err = info.BuildFromFilePath(args.Root)
		if err != nil {
			return
		}
		if args.InfoName != nil {
			info.Name = *args.InfoName
		}
		mi.InfoBytes, err = bencode.Marshal(info)
		if err != nil {
			return
		}
		err = mi.Write(os.Stdout)
		return
	}
	return
}
