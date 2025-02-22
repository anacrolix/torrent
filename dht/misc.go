package dht

import (
	"net"

	"github.com/anacrolix/missinggo/v2/iter"

	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/dht/v2/types"
)

func mustListen(addr string) net.PacketConn {
	ret, err := net.ListenPacket("udp", addr)
	if err != nil {
		panic(err)
	}
	return ret
}

func addrResolver(addr string) func() ([]Addr, error) {
	return func() ([]Addr, error) {
		ua, err := net.ResolveUDPAddr("udp", addr)
		return []Addr{NewAddr(ua)}, err
	}
}

type addrMaybeId = types.AddrMaybeId

func randomIdInBucket(rootId int160.T, bucketIndex int) int160.T {
	id := int160.FromByteArray(krpc.RandomNodeID())
	for i := range iter.N(bucketIndex) {
		id.SetBit(i, rootId.GetBit(i))
	}
	id.SetBit(bucketIndex, !rootId.GetBit(bucketIndex))
	return id
}
