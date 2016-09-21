package main

import (
	"fmt"
	"os"

	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent/metainfo"
)

func main() {
	tagflag.Parse(nil)

	mi, err := metainfo.Load(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading metainfo from stdin: %s", err)
		os.Exit(1)
	}
	info := mi.UnmarshalInfo()

	fmt.Fprintf(os.Stdout, "%s\n", mi.Magnet(info.Name, mi.HashInfoBytes()).String())
}
