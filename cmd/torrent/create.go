package main

import (
	"os"

	"github.com/anacrolix/bargle/v2"
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
		InfoName          *string
		PieceLength       tagflag.Bytes
		Url               []string
		Private           *bool
		Root              string
	}
	var haveRoot bool
	bargle.ParseAll(p,
		desc("extra announce-list tier entry",
			sliceLong("announce-list", &args.AnnounceList, bargle.BuiltinUnmarshaler[string])),
		desc("exclude default announce-list entries", boolFlag("empty-announce-list", &args.EmptyAnnounceList)),
		desc("comment", builtinLong("comment", &args.Comment)),
		desc("created by", builtinLong("created-by", &args.CreatedBy)),
		desc("override info name (defaults to ROOT)",
			bargle.Long("info-name", pointerUnmarshaler(&args.InfoName, bargle.BuiltinUnmarshaler[string]))),
		desc("piece length", bargle.Long("piece-length", bytesUnmarshaler(&args.PieceLength))),
		desc("add webseed url", sliceLong("url", &args.Url, bargle.BuiltinUnmarshaler[string])),
		desc("set the private flag in the info", boolPtrFlag("private", &args.Private)),
		positional("root", &haveRoot, bargle.BuiltinUnmarshaler(&args.Root)),
	)
	requireArg(p, "root", haveRoot)
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
}
