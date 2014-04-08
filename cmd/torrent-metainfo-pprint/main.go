package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/nsf/libtorgo/torrent"
)

func main() {
	flag.Parse()
	for _, filename := range flag.Args() {
		metainfo, err := torrent.LoadFromFile(filename)
		if err != nil {
			log.Print(err)
			continue
		}
		fmt.Printf("%+#v\n", metainfo)
	}
}
