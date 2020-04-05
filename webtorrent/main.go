package main

import (
	"log"
)

func main() {
	wt, err := NewClient()
	if err != nil {
		log.Fatalf("failed to create client: %v", err)
	}
	err = wt.LoadFile("./sintel.torrent")
	if err != nil {
		log.Fatalf("failed to load file: %v", err)
	}
	err = wt.Run()
	if err != nil {
		log.Fatalf("failed to run: %v", err)
	}
}
