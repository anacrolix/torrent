package main

import (
	"bitbucket.org/anacrolix/go.torrent"
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"github.com/davecheney/profile"
	metainfo "github.com/nsf/libtorgo/torrent"
	"launchpad.net/gommap"
	"log"
	"os"
	"path/filepath"
)

var (
	filePath = flag.String("torrent", "/path/to/the.torrent", "path of the torrent file")
	dirPath  = flag.String("path", "/torrent/data", "path of the torrent data")
)

func init() {
	flag.Parse()
}

func main() {
	defer profile.Start(profile.CPUProfile).Stop()
	metaInfo, err := metainfo.LoadFromFile(*filePath)
	if err != nil {
		log.Fatal(err)
	}
	devZero, err := os.Open("/dev/zero")
	if err != nil {
		log.Print(err)
	}
	defer devZero.Close()
	var mMapSpan torrent.MMapSpan
	for _, file := range metaInfo.Files {
		filename := filepath.Join(append([]string{*dirPath, metaInfo.Name}, file.Path...)...)
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
		mMapSpan = append(mMapSpan, torrent.MMap{goMMap})
	}
	log.Println(len(metaInfo.Files))
	log.Println(mMapSpan.Size())
	log.Println(len(metaInfo.Pieces))
	for piece := 0; piece < (len(metaInfo.Pieces)+sha1.Size-1)/sha1.Size; piece++ {
		expectedHash := metaInfo.Pieces[sha1.Size*piece : sha1.Size*(piece+1)]
		if len(expectedHash) == 0 {
			break
		}
		hash := sha1.New()
		_, err := mMapSpan.WriteSectionTo(hash, int64(piece)*metaInfo.PieceLength, metaInfo.PieceLength)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(piece, bytes.Equal(hash.Sum(nil), expectedHash))
	}
}
