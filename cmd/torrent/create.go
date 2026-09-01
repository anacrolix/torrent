package main

import (
	"os"

	"github.com/anacrolix/bargle/v2"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

var builtinAnnounceList = [][]string{
	{"http://p4p.arenabg.com:1337/announce"},
	{"udp://tracker.opentrackr.org:1337/announce"},
	{"udp://tracker.openbittorrent.com:6969/announce"},
}

func createCmd(p *bargle.Parser) action {
	var args struct {
		AnnounceList      []string
		EmptyAnnounceList bool
		Comment           string
		CreatedBy         string
		InfoName          g.Option[string]
		PieceLength       tagflag.Bytes
		Url               []string
		Private           g.Option[bool]
		Root              string
	}
	root := bargle.Positional("root", bargle.BuiltinUnmarshaler(&args.Root))
	bargle.ParseAll(p,
		desc("extra announce-list tier entry",
			bargle.Long("announce-list",
				bargle.AppendSlice(&args.AnnounceList, bargle.BuiltinUnmarshaler[string]))),
		desc("exclude default announce-list entries", bargle.Flag("empty-announce-list", &args.EmptyAnnounceList)),
		desc("comment", bargle.Long("comment", bargle.BuiltinUnmarshaler(&args.Comment))),
		desc("created by", bargle.Long("created-by", bargle.BuiltinUnmarshaler(&args.CreatedBy))),
		desc("override info name (defaults to ROOT)",
			bargle.Long("info-name", bargle.BuiltinOptionUnmarshaler(&args.InfoName))),
		desc("piece length", bargle.Long("piece-length", bytesUnmarshaler(&args.PieceLength))),
		desc("add webseed url", bargle.Long("url", bargle.AppendSlice(&args.Url, bargle.BuiltinUnmarshaler[string]))),
		desc("set the private flag in the info", bargle.OptionFlag("private", &args.Private)),
		root,
	)
	p.Require(root)
	return func() (err error) {
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
			Private:     args.Private.ToPtr(),
		}
		err = info.BuildFromFilePath(args.Root)
		if err != nil {
			return
		}
		if args.InfoName.Ok {
			info.Name = args.InfoName.Value
		}
		mi.InfoBytes, err = bencode.Marshal(info)
		if err != nil {
			return
		}
		err = mi.Write(os.Stdout)
		return
	}
}
