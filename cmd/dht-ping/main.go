// Pings DHT nodes with the given network addresses.
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/anacrolix/torrent/dht"
)

type pingResponse struct {
	addr  string
	krpc  dht.Msg
	msgOk bool
	rtt   time.Duration
}

func main() {
	err := CommandLine.Parse(os.Args[1:])
	pingStrAddrs := CommandLine.Args()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if len(pingStrAddrs) == 0 {
		os.Stderr.WriteString("Error: no target UDP node addresses specified\n")
		usageMessageAndQuit()
	}
	s, err := dht.NewServer(nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("dht server on %s", s.Addr())
	pingResponsesChan := make(chan pingResponse)
	timeoutChan := make(chan struct{})
	go func() {
		for i, netloc := range pingStrAddrs {
			if i != 0 {
				time.Sleep(1 * time.Millisecond)
			}
			addr, err := net.ResolveUDPAddr("udp4", netloc)
			if err != nil {
				log.Fatal(err)
			}
			t, err := s.Ping(addr)
			if err != nil {
				log.Fatal(err)
			}
			start := time.Now()
			t.SetResponseHandler(func(addr string) func(dht.Msg, bool) {
				return func(resp dht.Msg, ok bool) {
					pingResponsesChan <- pingResponse{
						addr:  addr,
						krpc:  resp,
						rtt:   time.Now().Sub(start),
						msgOk: ok,
					}
				}
			}(netloc))
		}
		if *timeout >= 0 {
			time.Sleep(*timeout)
			close(timeoutChan)
		}
	}()
	responses := 0
pingResponsesLoop:
	for _ = range pingStrAddrs {
		select {
		case resp := <-pingResponsesChan:
			if !resp.msgOk {
				break
			}
			responses++
			fmt.Printf("%-65s %s\n", fmt.Sprintf("%x (%s):", resp.krpc.SenderID(), resp.addr), resp.rtt)
		case <-timeoutChan:
			break pingResponsesLoop
		}
	}
	fmt.Printf("%d/%d responses (%f%%)\n", responses, len(pingStrAddrs), 100*float64(responses)/float64(len(pingStrAddrs)))
}
