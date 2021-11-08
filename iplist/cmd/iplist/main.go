package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent/iplist"
)

func main() {
	flags := struct {
		tagflag.StartPos
		Ips []net.IP
	}{}
	tagflag.Parse(&flags)
	il, err := iplist.NewFromReader(os.Stdin)
	if err != nil {
		log.Fatalf("error loading ip list: %s", err)
	}
	log.Printf("loaded %d ranges", il.NumRanges())
	for _, ip := range flags.Ips {
		r, ok := il.Lookup(ip)
		if ok {
			fmt.Printf("%s is in %v\n", ip, r)
		} else {
			fmt.Printf("%s not found\n", ip)
		}
	}
}
