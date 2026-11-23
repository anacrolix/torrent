package actor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Turns must never overlap, whether they come from Post, a queued Call or an inline Call. Run this
// with the race detector: counter has no other protection.
func TestTurnsDoNotOverlap(t *testing.T) {
	var counter, batches int
	a := New(nil, func() { batches++ })
	const senders, perSender = 8, 2000
	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() {
			for i := range perSender {
				if i%2 == 0 {
					a.Post(func() { counter++ })
				} else if !a.Call(func() { counter++ }) {
					t.Error("Call reported closed")
				}
			}
		})
	}
	wg.Wait()
	a.Close()
	if counter != senders*perSender {
		t.Fatalf("got %v turns, want %v", counter, senders*perSender)
	}
	if batches == 0 {
		t.Fatal("afterBatch never ran")
	}
}

func TestPostKeepsOrder(t *testing.T) {
	a := New(nil, nil)
	var got []int
	for i := range 1000 {
		a.Post(func() { got = append(got, i) })
	}
	a.Close()
	for i, v := range got {
		if v != i {
			t.Fatalf("turn %v ran in position %v", v, i)
		}
	}
	if len(got) != 1000 {
		t.Fatalf("ran %v turns", len(got))
	}
}

// A turn can post to its own Actor, and Close runs what was queued before it.
func TestPostFromTurn(t *testing.T) {
	a := New(nil, nil)
	ran := make(chan struct{})
	a.Post(func() {
		a.Post(func() { close(ran) })
	})
	<-ran
	a.Close()
	if a.Call(func() { t.Error("ran after Close") }) {
		t.Fatal("Call succeeded after Close")
	}
	a.Post(func() { t.Error("ran after Close") })
}

// Stands in for handling one message against shared state: about the cost of handling a Have.
type sharedState struct {
	words [512]uint64
	n     uint64
}

func (s *sharedState) apply(msg uint64) {
	for i := range 24 {
		s.words[(msg+uint64(i)*61)%512] += msg ^ s.n
	}
	s.n++
}

// Stands in for work a sender does on its own, like decoding a message.
func decode(seed uint64) uint64 {
	for range 16 {
		seed = seed*6364136223846793005 + 1442695040888963407
	}
	return seed
}

// Compares ways of getting messages from several senders applied to one piece of shared state.
// "mutex" is what the Client lock does today. Senders hand over batch messages at a time.
func BenchmarkHandoff(b *testing.B) {
	for _, batch := range []int{1, 16, 64} {
		for _, senders := range []int{1, 4, 16} {
			for _, how := range []string{"mutex", "chan", "post", "call"} {
				b.Run(fmt.Sprintf("batch=%d/senders=%d/%s", batch, senders, how), func(b *testing.B) {
					benchHandoff(b, how, senders, batch)
				})
			}
		}
	}
}

func benchHandoff(b *testing.B, how string, senders, batch int) {
	var (
		state sharedState
		mu    sync.Mutex
		a     *Actor
		ch    chan func()
		chWg  sync.WaitGroup
	)
	switch how {
	case "post", "call":
		a = New(nil, nil)
	case "chan":
		ch = make(chan func(), 256)
		chWg.Go(func() {
			for f := range ch {
				f()
			}
		})
	}
	// Each sender may have this many batches queued, as a conn would to bound memory.
	const credits = 4
	var remaining atomic.Int64
	remaining.Store(int64(b.N))
	var wg sync.WaitGroup
	b.ResetTimer()
	for s := range senders {
		wg.Go(func() {
			seed := uint64(s + 1)
			credit := make(chan struct{}, credits)
			type batchBuf struct {
				msgs []uint64
				run  func()
			}
			free := make(chan *batchBuf, credits+1)
			for range credits + 1 {
				bb := &batchBuf{msgs: make([]uint64, 0, batch)}
				bb.run = func() {
					for _, m := range bb.msgs {
						state.apply(m)
					}
					bb.msgs = bb.msgs[:0]
					free <- bb
					<-credit
				}
				free <- bb
			}
			for remaining.Add(int64(-batch)) > int64(-batch) {
				bb := <-free
				for range batch {
					seed = decode(seed)
					bb.msgs = append(bb.msgs, seed)
				}
				credit <- struct{}{}
				switch how {
				case "mutex":
					mu.Lock()
					bb.run()
					mu.Unlock()
				case "chan":
					ch <- bb.run
				case "post":
					a.Post(bb.run)
				case "call":
					a.Call(bb.run)
				}
			}
			// Wait for this sender's batches to be applied.
			for range credits {
				credit <- struct{}{}
			}
		})
	}
	wg.Wait()
	b.StopTimer()
	if a != nil {
		a.Close()
	}
	if ch != nil {
		close(ch)
		chWg.Wait()
	}
}
