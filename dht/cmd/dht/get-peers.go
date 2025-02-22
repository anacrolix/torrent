package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"

	"github.com/anacrolix/log"
	"golang.org/x/exp/constraints"
	"slices"

	"github.com/anacrolix/dht/v2"
)

func GetPeers(ctx context.Context, s *dht.Server, ih [20]byte, opts ...dht.AnnounceOpt) error {
	addrs := make(map[string]int)
	// PSA: Go sucks.
	a, err := s.AnnounceTraversal(ih, opts...)
	if err != nil {
		return err
	}
	defer a.Close()
	logger := log.ContextLogger(ctx)
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
				if _, ok := addrs[s]; !ok {
					logger.Levelf(log.Debug, "got peer %s for %x from %s", p, ih, ps.NodeInfo)
				}
				addrs[s]++
			}
			// TODO: Merge scrape blooms for final output
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
	ips := make(map[netip.Addr]struct{}, len(addrs))
	addrCountSlice := make([]addrFreq, 0, len(addrs))
	for addrStr, count := range addrs {
		addrPort := netip.MustParseAddrPort(addrStr)
		ips[addrPort.Addr()] = struct{}{}
		addrCountSlice = append(addrCountSlice, addrFreq{
			Addr:      addrPort,
			Frequency: count,
		})
	}
	slices.SortFunc(addrCountSlice, func(a, b addrFreq) int {
		// Looks like I got sick of anacrolix/multiless.
		return ordered(a.Frequency, b.Frequency).Then(
			lesser(a.Addr.Addr(), b.Addr.Addr())).Then(
			ordered(a.Addr.Port(), b.Addr.Port())).ToInt()
	})
	je := json.NewEncoder(os.Stdout)
	je.SetIndent("", "  ")
	return je.Encode(GetPeersOutput{
		Peers:           addrCountSlice,
		DistinctPeerIps: len(ips),
		TraversalStats:  a.TraversalStats(),
		ServerStats:     s.Stats(),
	})
}

type GetPeersOutput struct {
	Peers           []addrFreq
	DistinctPeerIps int
	TraversalStats  dht.TraversalStats
	ServerStats     dht.ServerStats
	// TODO: Scrape data
}

type addrFreq struct {
	Addr      netip.AddrPort
	Frequency int
}

func lesser[T interface{ Less(T) bool }](a, b T) Ordering {
	if a.Less(b) {
		return less(true)
	}
	if b.Less(a) {
		return less(false)
	}
	return equal
}

func ordered[T constraints.Ordered](a T, b T) Ordering {
	if a == b {
		return equal
	}
	return less(a < b)
}

var equal = Ordering{equal: true}

func less(a bool) Ordering { return Ordering{less: a} }

type Ordering struct {
	less  bool
	equal bool
}

func (me Ordering) Then(other Ordering) Ordering {
	if me.equal {
		return other
	} else {
		return me
	}
}

func (me Ordering) ToInt() int {
	if me.equal {
		return 0
	} else if me.less {
		return -1
	} else {
		return 1
	}
}
