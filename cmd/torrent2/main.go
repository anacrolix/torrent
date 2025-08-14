// This is an alternate to cmd/torrent which has become bloated with awful argument parsing. Since
// this is my most complicated binary, I will try to build something that satisfies only what I need
// here.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/anacrolix/bargle/v2"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
)

func assertOk(err error) {
	if err != nil {
		panic(err)
	}
}

func bail(str string) {
	panic(str)
}

func main() {
	err := mainErr()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func mainErr() error {
	p := bargle.NewParser()
	defer p.DoHelpIfHelping()
	runMap := func(m map[string]func()) {
		for key, value := range m {
			if p.Parse(bargle.Keyword(key)) {
				value()
				return
			}
		}
		p.Fail()
	}
	parseFileName := func() (ret string) {
		if p.Parse(bargle.Positional("file", bargle.BuiltinUnmarshaler(&ret))) {
			return
		}
		p.SetError(errors.New("file not specified"))
		panic(p.Fail())
	}
	runMap(map[string]func(){
		"metainfo": func() {
			runMap(map[string]func(){
				"validate-v2": func() {
					mi, err := metainfo.LoadFromFile(parseFileName())
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
					mi, err := metainfo.LoadFromFile(parseFileName())
					assertOk(err)
					info, err := mi.UnmarshalInfo()
					assertOk(err)
					fmt.Printf("name: %q\n", info.Name)
					fmt.Printf("# files:\n")
					files := info.UpvertedFiles()
					pieceIndex := 0
					for _, f := range files {
						numPieces := int((f.Length + info.PieceLength - 1) / info.PieceLength)
						endIndex := pieceIndex + numPieces
						hash := "no v2 pieces root"
						for a := range f.PiecesRoot.Iter() {
							hash = a.HexString()
						}
						fmt.Printf(
							"%s: %v: pieces (%v-%v)\n",
							hash,
							func() any {
								if info.IsDir() {
									return fmt.Sprintf("%q", f.BestPath())
								}
								return "(single file torrent)"
							}(),
							pieceIndex,
							endIndex-1,
						)
						pieceIndex = endIndex
					}
				},
			})
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
	})
	p.FailIfArgsRemain()
	return p.Err()
}
