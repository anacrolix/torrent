package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/anacrolix/libtorgo/metainfo"
)

func main() {
	flag.Parse()
	for _, filename := range flag.Args() {
		metainfo, err := metainfo.LoadFromFile(filename)
		if err != nil {
			log.Print(err)
			continue
		}
		fmt.Printf("%+#v\n", metainfo)
	}
}
