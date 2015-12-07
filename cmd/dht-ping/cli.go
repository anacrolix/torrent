package main

import (
	"flag"
	"fmt"
	"os"
)

const (
	usageDoc = `dht-ping sends a Bittorrent DHT protocol "ping" message to the specified UDP
address(es) (like router.bittorrent.com:6881) and reports the response
it receives.
`
)

var (
	CommandLine = flag.NewFlagSet("dht-ping", 0)
	timeout     = CommandLine.Duration("timeout", -1, "maximum timeout")
)

func init() {
	CommandLine.Usage = usageMessageAndQuit
}

func usageMessageAndQuit() {
	fmt.Printf("\nusage: dht-ping [-timeout] <node addresses>\n")
	fmt.Printf("example: dht-ping router.bittorrent.com:6881\n")
	flag.PrintDefaults()
	fmt.Printf(usageDoc)
	os.Exit(0)
}
