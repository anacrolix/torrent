package main

import (
	"flag"
	"fmt"
	"github.com/nsf/libtorgo/torrent"
	"log"
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
