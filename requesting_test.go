package torrent

import (
	"testing"

	pp "github.com/anacrolix/torrent/peer_protocol"
	qt "github.com/frankban/quicktest"
)

func keysAsSlice(m map[Request]struct{}) (sl []Request) {
	for k := range m {
		sl = append(sl, k)
	}
	return
}

func makeTypicalRequests() map[Request]struct{} {
	m := make(map[Request]struct{})
	for p := pp.Integer(0); p < 4; p++ {
		for c := pp.Integer(0); c < 16; c++ {
			m[Request{p, ChunkSpec{c * defaultChunkSize, defaultChunkSize}}] = struct{}{}
		}
	}
	return m
}

func TestLogExampleRequestMapOrdering(t *testing.T) {
	for k := range makeTypicalRequests() {
		t.Log(k)
	}
}

func TestRequestMapOrderingPersistent(t *testing.T) {
	m := makeTypicalRequests()
	// Shows that map order is persistent across separate range statements.
	qt.Assert(t, keysAsSlice(m), qt.ContentEquals, keysAsSlice(m))
}

func TestRequestMapOrderAcrossInstances(t *testing.T) {
	// This shows that different map instances with the same contents can have the same range order.
	qt.Assert(t, keysAsSlice(makeTypicalRequests()), qt.ContentEquals, keysAsSlice(makeTypicalRequests()))
}
