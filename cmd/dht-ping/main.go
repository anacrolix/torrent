// dht-ping is a cli tool that pings DHT nodes with the given UDP network
// addresses and returns NodeID if sucessful
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

	if len(pingStrAddrs) == 0 {
		os.Stderr.WriteString("Error: no target UDP node addresses specified\n")
		usageMessageAndQuit()
	}

	err = sendPings(pingStrAddrs)
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		os.Exit(0)
	}
}

// PingAddresses attempts to send the ping KRPC messages to the UDP addresses
func sendPings(targets []string) error {
	s, err := dht.NewServer(nil)
	if err != nil {
		return fmt.Errorf("cannot access UDP server for the dht node: %s", err)
	}
	pingResponsesChan := make(chan pingResponse)
	timeoutChan := make(chan struct{})
	go func() {
		for i, netloc := range targets {
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
	for _ = range targets {
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
		fmt.Printf("%d/%d responses (%f%%)\n", responses, len(targets), 100*float64(responses)/float64(len(targets)))

	}
	return nil
}
