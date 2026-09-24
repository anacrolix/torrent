package torrent

import (
	"testing"

	"github.com/bradfitz/iter"
	qt "github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
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
			m[Request{Index: p, ChunkSpec: ChunkSpec{Begin: c * defaultChunkSize, Length: defaultChunkSize}}] = struct{}{}
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
	qt.Assert(t, qt.ContentEquals(keysAsSlice(m), keysAsSlice(m)))
}

func TestRequestMapOrderAcrossInstances(t *testing.T) {
	// This shows that different map instances with the same contents can have the same range order.
	qt.Assert(t, qt.ContentEquals(keysAsSlice(makeTypicalRequests()), keysAsSlice(makeTypicalRequests())))
}

func TestRequestCandidateLimitFor(t *testing.T) {
	tests := []struct {
		name               string
		chunksPerPiece     int
		nominalMaxRequests int
		want               int
	}{
		{name: "piece window dominates", chunksPerPiece: 128, nominalMaxRequests: 32, want: 256},
		{name: "peer pipeline dominates", chunksPerPiece: 16, nominalMaxRequests: 64, want: 256},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			qt.Assert(t, qt.Equals(
				requestCandidateLimitFor(test.chunksPerPiece, test.nominalMaxRequests),
				test.want,
			))
		})
	}
}

func TestMergeRequestCandidates(t *testing.T) {
	qt.Assert(t, qt.DeepEquals(
		mergeRequestCandidates([]RequestIndex{1, 2}, []RequestIndex{3, 4, 5}, 4),
		[]RequestIndex{1, 2, 3, 4},
	))
	qt.Assert(t, qt.DeepEquals(
		mergeRequestCandidates([]RequestIndex{1, 2, 3, 4}, []RequestIndex{5, 6}, 4),
		[]RequestIndex{1, 2, 3, 4},
	))
}

// Added for testing repeating loop iteration after shuffling in Peer.applyRequestState.
func TestForLoopRepeatItem(t *testing.T) {
	t.Run("ExplicitLoopVar", func(t *testing.T) {
		once := false
		var seen []int
		for i := 0; i < 4; i++ {
			seen = append(seen, i)
			if !once && i == 2 {
				once = true
				i--
				// Will i++ still run?
				continue
			}
		}
		// We can mutate i and it's observed by the loop. No special treatment of the loop var.
		qt.Assert(t, qt.DeepEquals(seen, []int{0, 1, 2, 2, 3}))
	})
	t.Run("Range", func(t *testing.T) {
		once := false
		var seen []int
		for i := range iter.N(4) {
			seen = append(seen, i)
			if !once && i == 2 {
				once = true
				// Can we actually modify the next value of i produced by the range?
				i-- //nolint:ineffassign // intentional: testing that range ignores mutation
				continue
			}
		}
		// Range ignores any mutation to i.
		qt.Assert(t, qt.DeepEquals(seen, []int{0, 1, 2, 3}))
	})
}
