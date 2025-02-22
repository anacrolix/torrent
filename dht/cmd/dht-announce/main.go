package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"sync"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	"github.com/anacrolix/tagflag"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/dht/v2"
)

func main() {
	code := mainErr()
	if code != 0 {
		os.Exit(code)
	}
}

func mainErr() int {
	flags := struct {
		Port   int
		Debug  bool
		Scrape bool
		Addr   string
		tagflag.StartPos
		Infohash [][20]byte
	}{}
	tagflag.Parse(&flags)
	if !flags.Debug {
		log.Default = log.Default.FilterLevel(log.Info)
	}
	s, err := dht.NewServer(func() *dht.ServerConfig {
		sc := dht.NewDefaultServerConfig()
		if flags.Addr != "" {
			conn, err := net.ListenPacket("udp", flags.Addr)
			if err != nil {
				panic(err)
			}
			sc.Conn = conn
		}
		sc.Logger = log.Default
		return sc
	}())
	if err != nil {
		log.Printf("error creating server: %s", err)
		return 1
	}
	log.Printf("dht server at %v", s)
	defer s.Close()
	var wg sync.WaitGroup
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	addrs := make(map[[20]byte]map[string]struct{}, len(flags.Infohash))
	for _, ih := range flags.Infohash {
		// PSA: Go sucks.
		a, err := s.Announce(ih, flags.Port, false, func() (ret []dht.AnnounceOpt) {
			if flags.Scrape {
				ret = append(ret, dht.Scrape())
			}
			return
		}()...)
		if err != nil {
			log.Printf("error announcing %s: %s", ih, err)
			continue
		}
		wg.Add(1)
		addrs[ih] = make(map[string]struct{})
		go func(ih [20]byte) {
			defer wg.Done()
			defer a.Close()
		getPeers:
			for {
				select {
				case <-ctx.Done():
					a.StopTraversing()
					break getPeers
				case ps, ok := <-a.Peers:
					if !ok {
						break getPeers
					}
					for _, p := range ps.Peers {
						s := p.String()
						if _, ok := addrs[ih][s]; !ok {
							log.Printf("got peer %s for %x from %s", p, ih, ps.NodeInfo)
							addrs[ih][s] = struct{}{}
						}
					}
					if bf := ps.BFpe; bf != nil {
						log.Printf("%v claims %v peers for %x", ps.NodeInfo, bf.EstimateCount(), ih)
					}
					if bf := ps.BFsd; bf != nil {
						log.Printf("%v claims %v seeds for %x", ps.NodeInfo, bf.EstimateCount(), ih)
					}
				}
			}
			log.Levelf(log.Debug, "finishing traversal")
			<-a.Finished()
			log.Printf("%v contacted %v nodes", a, a.NumContacted())
		}(ih)
	}
	wg.Wait()
	spew.Dump(s.Stats())
	for _, ih := range flags.Infohash {
		ips := make(map[string]struct{}, len(addrs[ih]))
		for s := range addrs[ih] {
			ip, _, err := net.SplitHostPort(s)
			if err != nil {
				log.Printf("error parsing addr: %s", err)
			}
			ips[ip] = struct{}{}
		}
		log.Printf("%x: %d addrs %d distinct ips", ih, len(addrs[ih]), len(ips))
	}
	return 0
}
