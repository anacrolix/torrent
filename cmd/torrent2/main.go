// This is an alternate to cmd/torrent which has become bloated with awful argument parsing. Since
// this is my most complicated binary, I will try to build something that satisfies only what I need
// here.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
)

type argError struct {
	err error
}

func assertOk(err error) {
	if err != nil {
		panic(err)
	}
}

func bail(str string) {
	panic(str)
}

func main() {
	args := os.Args[1:]
	map[string]func(){
		"metainfo": func() {
			map[string]func(){
				"validate-v2": func() {
					mi, err := metainfo.LoadFromFile(args[2])
					assertOk(err)
					info, err := mi.UnmarshalInfo()
					assertOk(err)
					if !info.HasV2() {
						bail("not a v2 torrent")
					}
					err = metainfo.ValidatePieceLayers(mi.PieceLayers, &info.FileTree, info.PieceLength)
					assertOk(err)
				},
				"pprint": func() {
					mi, err := metainfo.LoadFromFile(args[2])
					assertOk(err)
					info, err := mi.UnmarshalInfo()
					assertOk(err)
					files := info.UpvertedFiles()
					pieceIndex := 0
					for _, f := range files {
						numPieces := int((f.Length + info.PieceLength - 1) / info.PieceLength)
						endIndex := pieceIndex + numPieces
						fmt.Printf(
							"%x: %q: pieces (%v-%v)\n",
							f.PiecesRoot.Unwrap(),
							f.BestPath(),
							pieceIndex,
							endIndex-1,
						)
						pieceIndex = endIndex
					}
				},
			}[args[1]]()
		},
		"merkle": func() {
			h := merkle.NewHash()
			n, err := io.Copy(h, os.Stdin)
			log.Printf("copied %v bytes", n)
			if err != nil {
				panic(err)
			}
			fmt.Printf("%x\n", h.Sum(nil))
		},
	}[args[0]]()
}
