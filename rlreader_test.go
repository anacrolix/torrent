package torrent

import (
	"io"
	"log"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/bradfitz/iter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func writeN(ws []io.Writer, n int) error {
	b := make([]byte, n)
	for _, w := range ws[1:] {
		n1 := rand.Intn(n)
		wn, err := w.Write(b[:n1])
		if wn != n1 {
			if err == nil {
				panic(n1)
			}
			return err
		}
		n -= n1
	}
	wn, err := ws[0].Write(b[:n])
	if wn != n {
		if err == nil {
			panic(n)
		}
	}
	return err
}

func TestRateLimitReaders(t *testing.T) {
	const (
		numReaders     = 2
		bytesPerSecond = 100
		burst          = 5
		readSize       = 6
		writeRounds    = 10
		bytesPerRound  = 12
	)
	control := rate.NewLimiter(bytesPerSecond, burst)
	shared := rate.NewLimiter(bytesPerSecond, burst)
	var (
		ws []io.Writer
		cs []io.Closer
	)
	wg := sync.WaitGroup{}
	type read struct {
		N int
		// When the read was allowed.
		At time.Time
	}
	reads := make(chan read)
	done := make(chan struct{})
	for range iter.N(numReaders) {
		r, w := io.Pipe()
		ws = append(ws, w)
		cs = append(cs, w)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rateLimitedReader{
				l: shared,
				r: r,
			}
			b := make([]byte, readSize)
			for {
				n, err := r.Read(b)
				select {
				case reads <- read{n, r.lastRead}:
				case <-done:
					return
				}
				if err == io.EOF {
					return
				}
				if err != nil {
					panic(err)
				}
			}
		}()
	}
	closeAll := func() {
		for _, c := range cs {
			c.Close()
		}
	}
	defer func() {
		close(done)
		closeAll()
		wg.Wait()
	}()
	written := 0
	go func() {
		for range iter.N(writeRounds) {
			err := writeN(ws, bytesPerRound)
			if err != nil {
				log.Printf("error writing: %s", err)
				break
			}
			written += bytesPerRound
		}
		closeAll()
		wg.Wait()
		close(reads)
	}()
	totalBytesRead := 0
	started := time.Now()
	for r := range reads {
		totalBytesRead += r.N
		require.False(t, r.At.IsZero())
		// Copy what the reader should have done with its reservation.
		res := control.ReserveN(r.At, r.N)
		// If we don't have to wait with the control, the reader has gone too
		// fast.
		if res.Delay() > 0 {
			log.Printf("%d bytes not allowed at %s", r.N, time.Since(started))
			t.FailNow()
		}
	}
	assert.EqualValues(t, writeRounds*bytesPerRound, totalBytesRead)
}
