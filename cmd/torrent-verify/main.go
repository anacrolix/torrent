package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/anacrolix/torrent/mmap_span"
	"github.com/anacrolix/libtorgo/metainfo"
	"launchpad.net/gommap"
)

var (
	filePath = flag.String("torrent", "/path/to/the.torrent", "path of the torrent file")
	dirPath  = flag.String("path", "/torrent/data", "path of the torrent data")
)

func init() {
	flag.Parse()
}

func main() {
	metaInfo, err := metainfo.LoadFromFile(*filePath)
	if err != nil {
		log.Fatal(err)
	}
	devZero, err := os.Open("/dev/zero")
	if err != nil {
		log.Print(err)
	}
	defer devZero.Close()
	var mMapSpan *mmap_span.MMapSpan
	for _, file := range metaInfo.Info.Files {
		filename := filepath.Join(append([]string{*dirPath, metaInfo.Info.Name}, file.Path...)...)
		osFile, err := os.Open(filename)
		mmapFd := osFile.Fd()
		if err != nil {
			if pe, ok := err.(*os.PathError); ok && pe.Err.Error() == "no such file or directory" {
				mmapFd = devZero.Fd()
			} else {
				log.Fatal(err)
			}
		}
		goMMap, err := gommap.MapRegion(mmapFd, 0, file.Length, gommap.PROT_READ, gommap.MAP_PRIVATE)
		if err != nil {
			log.Fatal(err)
		}
		if int64(len(goMMap)) != file.Length {
			log.Printf("file mmap has wrong size: %#v", filename)
		}
		osFile.Close()
		mMapSpan.Append(goMMap)
	}
	log.Println(len(metaInfo.Info.Files))
	log.Println(mMapSpan.Size())
	log.Println(len(metaInfo.Info.Pieces))
	for piece := 0; piece < (len(metaInfo.Info.Pieces)+sha1.Size-1)/sha1.Size; piece++ {
		expectedHash := metaInfo.Info.Pieces[sha1.Size*piece : sha1.Size*(piece+1)]
		if len(expectedHash) == 0 {
			break
		}
		hash := sha1.New()
		_, err := mMapSpan.WriteSectionTo(hash, int64(piece)*metaInfo.Info.PieceLength, metaInfo.Info.PieceLength)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(piece, bytes.Equal(hash.Sum(nil), expectedHash))
	}
}
