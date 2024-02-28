// This is an alternate to cmd/torrent which has become bloated with awful argument parsing. Since
// this is my most complicated binary, I will try to build something that satisfies only what I need
// here.
package main

import (
	"os"

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
			}[args[1]]()
		},
	}[args[0]]()
}
