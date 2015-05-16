package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"launchpad.net/gommap"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mmap_span"
)

var (
	torrentPath = flag.String("torrent", "/path/to/the.torrent", "path of the torrent file")
	dataPath    = flag.String("path", "/torrent/data", "path of the torrent data")
	summary     = flag.Bool("summary", false, "display summary at the end")
)

func verifySummary(sMap map[bool][]int) {
	fmt.Println("----------------")
	fmt.Println(" TORRENT-VERIFY ")
	fmt.Println("----------------")
	fmt.Printf("Number of correct pieces: %d\n", len(sMap[true]))
	fmt.Printf("Number of wrong pieces: %d\n", len(sMap[false]))
}

func fileToMmap(filename string, length int64, devZero *os.File, mMapSpan *mmap_span.MMapSpan) {
	osFile, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	mmapFd := osFile.Fd()
	goMMap, err := gommap.MapRegion(mmapFd, 0, length, gommap.PROT_READ, gommap.MAP_PRIVATE)
	if err != nil {
		log.Fatal(err)
	}
	if int64(len(goMMap)) != length {
		log.Printf("file mmap has wrong size: %#v", filename)
	}
	osFile.Close()
	mMapSpan.Append(goMMap)
}

func main() {
	flag.Parse()
	summaryMap := make(map[bool][]int)
	metaInfo, err := metainfo.LoadFromFile(*torrentPath)
	if err != nil {
		log.Fatal(err)
	}
	devZero, err := os.Open("/dev/zero")
	if err != nil {
		log.Print(err)
	}
	defer devZero.Close()
	mMapSpan := &mmap_span.MMapSpan{}
	if len(metaInfo.Info.Files) > 0 {
		for _, file := range metaInfo.Info.Files {
			filename := filepath.Join(append([]string{*dataPath, metaInfo.Info.Name}, file.Path...)...)
			fileToMmap(filename, file.Length, devZero, mMapSpan)
		}
		log.Println(len(metaInfo.Info.Files))
	} else {
		fileToMmap(*dataPath, metaInfo.Info.Length, devZero, mMapSpan)
	}
	log.Println(mMapSpan.Size())
	log.Println(len(metaInfo.Info.Pieces))
	var pieceValid bool
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
		pieceValid = bytes.Equal(hash.Sum(nil), expectedHash)
		summaryMap[pieceValid] = append(summaryMap[pieceValid], piece)
		fmt.Println(piece, pieceValid)
	}
	if *summary {
		verifySummary(summaryMap)
	}
}
