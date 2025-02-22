// Pings DHT nodes with the given network addresses.
package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
)

type pingArgs struct {
	Network  string
	Timeout  time.Duration `help:"sets a timeout for all queries"`
	Defaults bool          `help:"include all the default bootstrap nodes"`
	Nodes    []string      `arg:"positional" arity:"*" help:"nodes to ping e.g. router.bittorrent.com:6881"`
}

func ping(args pingArgs, s *dht.Server) error {
	var wg sync.WaitGroup
	defaults := dht.DefaultGlobalBootstrapHostPorts
	if !args.Defaults {
		defaults = nil
	}
	for _, a := range append(args.Nodes, defaults...) {
		func(a string) {
			ua, err := net.ResolveUDPAddr(args.Network, a)
			if err != nil {
				log.Fatal(err)
			}
			started := time.Now()
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := s.Ping(ua)
				err := res.Err
				if err != nil {
					fmt.Printf("%s: %s: %s\n", a, time.Since(started), err)
					return
				}
				id := *res.Reply.SenderID()
				fmt.Printf("%s: %x %c: %s\n", a, id, func() rune {
					if dht.NodeIdSecure(id, ua.IP) {
						return '✔'
					} else {
						return '✘'
					}
				}(), time.Since(started))
			}()
		}(a)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timeout := make(chan struct{})
	if args.Timeout != 0 {
		go func() {
			time.Sleep(args.Timeout)
			close(timeout)
		}()
	}
	select {
	case <-done:
		return nil
	case <-timeout:
		return errors.New("timed out")
	}
}
