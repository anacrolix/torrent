package torrent

import (
	"container/heap"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
	"unsafe"

	qt "github.com/go-quicktest/qt"
)

func TestWorseConnLastHelpful(t *testing.T) {
	qt.Check(t, qt.IsTrue((&worseConnInput{}).Less(&worseConnInput{LastHelpful: time.Now()})))
	qt.Check(t, qt.IsTrue((&worseConnInput{}).Less(&worseConnInput{CompletedHandshake: time.Now()})))
	qt.Check(t, qt.IsFalse((&worseConnInput{LastHelpful: time.Now()}).Less(&worseConnInput{CompletedHandshake: time.Now()})))
	qt.Check(t, qt.IsTrue((&worseConnInput{
		LastHelpful: time.Now(),
	}).Less(&worseConnInput{
		LastHelpful:        time.Now(),
		CompletedHandshake: time.Now(),
	})))
	now := time.Now()
	qt.Check(t, qt.IsFalse((&worseConnInput{
		LastHelpful: now,
	}).Less(&worseConnInput{
		LastHelpful:        now.Add(-time.Nanosecond),
		CompletedHandshake: now,
	})))
	readyPeerPriority := func() (peerPriority, error) {
		return 42, nil
	}
	qt.Check(t, qt.IsTrue((&worseConnInput{
		GetPeerPriority: readyPeerPriority,
	}).Less(&worseConnInput{
		GetPeerPriority: readyPeerPriority,
		Pointer:         1,
	})))
	qt.Check(t, qt.IsFalse((&worseConnInput{
		GetPeerPriority: readyPeerPriority,
		Pointer:         2,
	}).Less(&worseConnInput{
		GetPeerPriority: readyPeerPriority,
		Pointer:         1,
	})))
}

// TestWorseConnPriorityError verifies that a left-side peer-priority error still falls back to
// pointer ordering without forcing the right-side priority lookup.
func TestWorseConnPriorityError(t *testing.T) {
	rightCalls := 0
	left := worseConnInput{
		GetPeerPriority: func() (peerPriority, error) {
			return 0, errPriorityLookup
		},
		Pointer: 1,
	}
	right := worseConnInput{
		GetPeerPriority: func() (peerPriority, error) {
			rightCalls++
			return 42, nil
		},
		Pointer: 2,
	}
	qt.Check(t, qt.IsTrue(left.Less(&right)))
	qt.Check(t, qt.Equals(rightCalls, 0))
}

// TestWorseConnSameInputPanic verifies that the comparison still panics when every ordering field,
// including the pointer tie-breaker, is identical.
func TestWorseConnSameInputPanic(t *testing.T) {
	input := worseConnInput{
		GetPeerPriority: func() (peerPriority, error) {
			return 42, nil
		},
	}
	qt.Check(t, qt.PanicMatches(func() {
		input.Less(&input)
	}, "cannot differentiate.*"))
}

var errPriorityLookup = errors.New("priority lookup failed")

// TestWorseConnSliceHeapOrder checks the heap pops conns worst-first, exercising the index-based
// Less, Swap and Pop against key storage that Swap doesn't permute.
func TestWorseConnSliceHeapOrder(t *testing.T) {
	const numConns = 16
	wcs := worseConnSlice{
		conns:      make([]*PeerConn, numConns),
		keys:       make([]*worseConnInput, numConns),
		keyStorage: make([]worseConnInput, numConns),
	}
	base := time.Now()
	ranks := make(map[*PeerConn]int, numConns)
	for i := range wcs.conns {
		wcs.conns[i] = new(PeerConn)
		// 7 is coprime with numConns, so this permutes the ranks and the initial slice order is
		// neither sorted nor reversed.
		rank := (i*7 + 3) % numConns
		ranks[wcs.conns[i]] = rank
		wcs.keyStorage[i] = worseConnInput{
			LastHelpful: base.Add(time.Duration(rank) * time.Second),
			Pointer:     uintptr(rank + 1),
		}
		wcs.keys[i] = &wcs.keyStorage[i]
	}
	heap.Init(&wcs)
	var popped []int
	for wcs.Len() != 0 {
		popped = append(popped, ranks[heap.Pop(&wcs).(*PeerConn)])
	}
	// The oldest LastHelpful is the worst conn, so ranks come out ascending.
	expected := make([]int, 0, numConns)
	for i := range numConns {
		expected = append(expected, i)
	}
	qt.Check(t, qt.DeepEquals(popped, expected))
	qt.Check(t, qt.HasLen(wcs.conns, 0))
	qt.Check(t, qt.HasLen(wcs.keys, 0))
}

// TestWorseConnSliceInitKeys checks keys are snapshotted from the peers they're built for, and that
// draining the heap yields every conn exactly once.
func TestWorseConnSliceInitKeys(t *testing.T) {
	cl := newTestingClient(t)
	tor := cl.newTorrentForTesting()
	wcs := worseConnSlice{conns: newTestingPeerConns(cl, tor, 8)}
	wcs.initKeys(worseConnLensOpts{incomingIsBad: true})
	for i, c := range wcs.conns {
		qt.Assert(t, qt.Equals(wcs.keys[i], &wcs.keyStorage[i]))
		qt.Check(t, qt.Equals(wcs.keys[i].Pointer, uintptr(unsafe.Pointer(c))))
		qt.Check(t, qt.Equals(wcs.keys[i].CompletedHandshake, c.completedHandshake))
		// All the test conns are incoming, and incomingIsBad was set.
		qt.Check(t, qt.IsTrue(wcs.keys[i].BadDirection))
	}
	remaining := make(map[*PeerConn]struct{}, len(wcs.conns))
	for _, c := range wcs.conns {
		remaining[c] = struct{}{}
	}
	heap.Init(&wcs)
	for wcs.Len() != 0 {
		c := heap.Pop(&wcs).(*PeerConn)
		_, ok := remaining[c]
		qt.Assert(t, qt.IsTrue(ok))
		delete(remaining, c)
	}
	qt.Check(t, qt.HasLen(remaining, 0))
}

// TestWorseConnSliceDropPeerRefs checks a worseConnSlice that has been through a partial drain
// doesn't retain peers, since it's pooled for reuse.
func TestWorseConnSliceDropPeerRefs(t *testing.T) {
	cl := newTestingClient(t)
	tor := cl.newTorrentForTesting()
	wcs := worseConnSlice{conns: newTestingPeerConns(cl, tor, 8)}
	wcs.initKeys(worseConnLensOpts{})
	heap.Init(&wcs)
	heap.Pop(&wcs)
	heap.Pop(&wcs)
	wcs.dropPeerRefs()
	qt.Check(t, qt.IsNil(wcs.conns))
	// The trimmed tails have to be cleared too, not just the live prefixes.
	for _, key := range wcs.keys[:cap(wcs.keys)] {
		qt.Check(t, qt.IsNil(key))
	}
	for _, key := range wcs.keyStorage[:cap(wcs.keyStorage)] {
		qt.Check(t, qt.IsNil(key.GetPeerPriority))
		qt.Check(t, qt.Equals(key.Pointer, 0))
	}
}

// newTestingPeerConns adds distinct incoming conns to the Torrent and returns them.
func newTestingPeerConns(cl *Client, tor *Torrent, num int) (ret []*PeerConn) {
	ret = make([]*PeerConn, 0, num)
	for i := range num {
		c := cl.newConnection(nil, newConnectionOpts{
			network:    "test",
			remoteAddr: &net.TCPAddr{IP: net.IPv4(1, 2, 3, byte(i+1)), Port: 1024 + i},
			connString: fmt.Sprintf("test-%v", i),
		})
		c.setTorrent(tor)
		c.completedHandshake = time.Now()
		tor.conns[c] = struct{}{}
		ret = append(ret, c)
	}
	return
}

func BenchmarkWorstBadConn(b *testing.B) {
	for _, numConns := range []int{16, 64, 256} {
		b.Run(fmt.Sprintf("%vConns", numConns), func(b *testing.B) {
			cl := newTestingClient(b)
			tor := cl.newTorrentForTesting()
			newTestingPeerConns(cl, tor, numConns)
			tor.maxEstablishedConns = numConns
			opts := worseConnLensOpts{}
			b.ReportAllocs()
			for b.Loop() {
				// Conns all completed their handshake just now, so nothing is droppable and the
				// whole heap is drained.
				if tor.worstBadConn(opts) != nil {
					b.Fatal("expected no bad conn")
				}
			}
		})
	}
}
