package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/tagflag"
	"github.com/bradfitz/iter"

	"github.com/anacrolix/torrent/metainfo"
)

var flags struct {
	JustName    bool
	PieceHashes bool
	Files       bool
	tagflag.StartPos
}

func processReader(r io.Reader) error {
	metainfo, err := metainfo.Load(r)
	if err != nil {
		return err
	}
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
	if len(metainfo.Nodes) >  0 {
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

func main() {
	defer envpprof.Stop()
	tagflag.Parse(&flags)
	err := processReader(bufio.NewReader(os.Stdin))
	if err != nil {
		log.Fatal(err)
	}
	if !flags.JustName {
		os.Stdout.WriteString("\n")
	}
}
