package types

import (
	"math/rand"
	"net"
	"testing"

	"github.com/bradfitz/iter"
	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/dht/v2/krpc"
)

func TestNoIdFarther(tb *testing.T) {
	c := qt.New(tb)
	var a AddrMaybeId
	a.FromNodeInfo(krpc.RandomNodeInfo(16))
	target := krpc.RandomNodeID().Int160()
	b := a
	c.Assert(a.CloserThan(b, target), qt.IsFalse)
	b.Id.SetNone()
	c.Assert(a.CloserThan(b, target), qt.IsTrue)
	c.Assert(b.CloserThan(a, target), qt.IsFalse)
	c.Assert(b.CloserThan(b, target), qt.IsFalse)
	b.Id.SetSomeZeroValue()
	b.Id = a.Id
	c.Assert(a.CloserThan(b, target), qt.IsFalse)
	id := a.Id.UnwrapPtr()
	for i := range iter.N(160) {
		if target.GetBit(i) != id.GetBit(i) {
			id.SetBit(i, target.GetBit(i))
			break
		}
	}
	tb.Log(a)
	tb.Log(b)
	tb.Log(target)
	c.Assert(a.CloserThan(b, target), qt.IsTrue)
}

func TestCloserThanId(tb *testing.T) {
	c := qt.New(tb)
	var a AddrMaybeId
	a.FromNodeInfo(krpc.RandomNodeInfo(16))
	target := krpc.RandomNodeID().Int160()
	c.Assert(a.CloserThan(a, target), qt.IsFalse)
	b := a
	b.Id.SetSomeZeroValue()
	b.Id = a.Id
	c.Assert(a.CloserThan(b, target), qt.IsFalse)
	for i := range iter.N(160) {
		if target.GetBit(i) != a.Id.UnwrapPtr().GetBit(i) {
			a.Id.UnwrapPtr().SetBit(i, target.GetBit(i))
			break
		}
	}
	tb.Log(a)
	tb.Log(b)
	tb.Log(target)
	c.Assert(a.CloserThan(b, target), qt.IsTrue)
}

func BenchmarkDeterministicAddr(tb *testing.B) {
	ip := net.ParseIP("1.2.3.4")
	target := krpc.RandomNodeID().Int160()
	for range iter.N(tb.N) {
		a := AddrMaybeId{
			Addr: krpc.NodeAddr{
				IP:   ip,
				Port: rand.Int(),
			}.ToNodeAddrPort(),
		}
		b := AddrMaybeId{
			Addr: krpc.NodeAddr{
				IP:   ip,
				Port: rand.Int(),
			}.ToNodeAddrPort(),
		}
		if a.CloserThan(b, target) != a.CloserThan(b, target) {
			tb.Fatal("not deterministic")
		}
	}
}
