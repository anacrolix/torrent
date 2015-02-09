package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/anacrolix/libtorgo/metainfo"
)

func main() {
	name := flag.Bool("name", false, "print name")
	flag.Parse()
	for _, filename := range flag.Args() {
		metainfo, err := metainfo.LoadFromFile(filename)
		if err != nil {
			log.Print(err)
			continue
		}
		if *name {
			fmt.Printf("%s\n", metainfo.Info.Name)
		} else {
			fmt.Printf("%+#v\n", metainfo)
		}
	}
}
