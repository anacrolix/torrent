package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/bradfitz/iter"
	"github.com/edsrzf/mmap-go"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mmap_span"
)

var (
	torrentPath = flag.String("torrent", "/path/to/the.torrent", "path of the torrent file")
	dataPath    = flag.String("path", "/torrent/data", "path of the torrent data")
)

func fileToMmap(filename string, length int64) mmap.MMap {
	osFile, err := os.Open(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		log.Fatal(err)
	}
	goMMap, err := mmap.MapRegion(osFile, int(length), mmap.RDONLY, mmap.COPY, 0)
	if err != nil {
		log.Fatal(err)
	}
	if int64(len(goMMap)) != length {
		log.Printf("file mmap has wrong size: %#v", filename)
	}
	osFile.Close()

	return goMMap
}

func main() {
	log.SetFlags(log.Flags() | log.Lshortfile)
	flag.Parse()
	metaInfo, err := metainfo.LoadFromFile(*torrentPath)
	if err != nil {
		log.Fatal(err)
	}
	info := metaInfo.UnmarshalInfo()
	mMapSpan := &mmap_span.MMapSpan{}
	if len(info.Files) > 0 {
		for _, file := range info.Files {
			filename := filepath.Join(append([]string{*dataPath, info.Name}, file.Path...)...)
			goMMap := fileToMmap(filename, file.Length)
			mMapSpan.Append(goMMap)
		}
		log.Println(len(info.Files))
	} else {
		goMMap := fileToMmap(*dataPath, info.Length)
		mMapSpan.Append(goMMap)
	}
	log.Println(mMapSpan.Size())
	log.Println(len(info.Pieces))
	for i := range iter.N(info.NumPieces()) {
		p := info.Piece(i)
		hash := sha1.New()
		_, err := io.Copy(hash, io.NewSectionReader(mMapSpan, p.Offset(), p.Length()))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d: %x: %v\n", i, p.Hash(), bytes.Equal(hash.Sum(nil), p.Hash().Bytes()))
	}
}
