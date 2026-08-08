package torrent

import (
	"bytes"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/log"
	"github.com/dustin/go-humanize"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// Collects writer output and signals each write.
type msgWriterSink struct {
	mu    stdsync.Mutex
	buf   bytes.Buffer
	wrote chan struct{}
}

func newMsgWriterSink() *msgWriterSink {
	return &msgWriterSink{wrote: make(chan struct{}, 1)}
}

func (me *msgWriterSink) Write(b []byte) (int, error) {
	me.mu.Lock()
	me.buf.Write(b)
	me.mu.Unlock()
	select {
	case me.wrote <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (me *msgWriterSink) bytes() []byte {
	me.mu.Lock()
	defer me.mu.Unlock()
	return append([]byte(nil), me.buf.Bytes()...)
}

// Runs the writer and returns an idempotent stop func that joins it. Also registered as cleanup.
func startMsgWriter(t *testing.T, w *peerConnMsgWriter, closed *chansync.SetOnce, keepAliveTimeout time.Duration) func() {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(keepAliveTimeout)
	}()
	var stopOnce stdsync.Once
	stop := func() {
		stopOnce.Do(func() {
			closed.Set()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("message writer did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// A writeCond Broadcast issued after fillWriteBuffer has checked for pending work, but before the
// writer parks, must not be lost: production never re-tickles while a request-update reason is
// pending. The keepalive timeout is high so recovery can only come from the wakeup itself.
func TestMsgWriterTickleDuringFillNotLost(t *testing.T) {
	var (
		closed  chansync.SetOnce
		fills   atomic.Int32
		pending atomic.Bool
		w       *peerConnMsgWriter
	)
	sink := newMsgWriterSink()
	checked := make(chan struct{})
	release := make(chan struct{})
	w = &peerConnMsgWriter{
		fillWriteBuffer: func() {
			switch fills.Add(1) {
			case 1:
				// The check finds nothing pending, then work arrives before the writer parks.
				if pending.Load() {
					t.Error("work pending before the first check")
				}
				close(checked)
				select {
				case <-release:
				case <-closed.Done():
				}
			default:
				if pending.CompareAndSwap(true, false) {
					w.mu.Lock()
					w.writeBuffer.WriteByte(0)
					w.mu.Unlock()
				}
			}
		},
		closed:      &closed,
		logger:      log.Default,
		w:           sink,
		keepAlive:   func() bool { return false },
		writeBuffer: new(peerConnMsgWriterBuffer),
	}
	stop := startMsgWriter(t, w, &closed, time.Hour)
	select {
	case <-checked:
	case <-time.After(10 * time.Second):
		t.Fatal("writer never entered fillWriteBuffer")
	}
	pending.Store(true)
	w.writeCond.Broadcast()
	close(release)
	select {
	case <-sink.wrote:
	case <-time.After(10 * time.Second):
		t.Fatal("wakeup between fill and park was lost: writer never wrote")
	}
	stop()
	if got := sink.bytes(); !bytes.Equal(got, []byte{0}) {
		t.Fatalf("unexpected writer output: %x", got)
	}
}

// After the keepalive timer fires without anything to write, it must be armed again: a connection
// that later becomes useful gets its keepalive from the next expiration, with no tickle involved.
func TestMsgWriterKeepAliveTimerRearmed(t *testing.T) {
	const keepAliveTimeout = 20 * time.Millisecond
	var (
		closed          chansync.SetOnce
		keepAliveChecks atomic.Int32
		keepAliveNow    atomic.Bool
	)
	sink := newMsgWriterSink()
	firstExpiredCheck := make(chan struct{})
	w := &peerConnMsgWriter{
		fillWriteBuffer: func() {},
		closed:          &closed,
		logger:          log.Default,
		w:               sink,
		keepAlive: func() bool {
			// Capture before signaling, so the check following the first timer expiration is
			// guaranteed to have observed false. Consuming the flag caps output at one keepalive,
			// keeping the final output assertion exact regardless of scheduling.
			check := keepAliveChecks.Add(1)
			useful := keepAliveNow.CompareAndSwap(true, false)
			if check == 2 {
				close(firstExpiredCheck)
			}
			return useful
		},
		writeBuffer: new(peerConnMsgWriterBuffer),
	}
	stop := startMsgWriter(t, w, &closed, keepAliveTimeout)
	// The check following the first timer expiration has run and returned false: the single-shot
	// timer has been consumed while the connection wasn't useful.
	select {
	case <-firstExpiredCheck:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first timer expiration")
	}
	keepAliveNow.Store(true)
	select {
	case <-sink.wrote:
	case <-time.After(10 * time.Second):
		t.Fatal("keepalive never written: timer was not rearmed after firing")
	}
	stop()
	if got, want := sink.bytes(), (pp.Message{Keepalive: true}).MustMarshalBinary(); !bytes.Equal(got, want) {
		t.Fatalf("unexpected writer output: got %x, want %x", got, want)
	}
}

func PieceMsg(length int64) pp.Message {
	return pp.Message{
		Type:  pp.Piece,
		Index: pp.Integer(0),
		Begin: pp.Integer(0),
		Piece: make([]byte, length),
	}
}

var benchmarkPieceLengths = []int{defaultChunkSize, 1 << 20, 4 << 20, 8 << 20}

func runBenchmarkWriteToBuffer(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: new(peerConnMsgWriterBuffer),
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		//b.StopTimer()
		writer.writeBuffer.Reset()
		//b.StartTimer()
		writer.writeToBuffer(msg)
	}
}

func BenchmarkWritePieceMsg(b *testing.B) {
	for _, length := range benchmarkPieceLengths {
		b.Run(humanize.IBytes(uint64(length)), func(b *testing.B) {
			b.Run("ToBuffer", func(b *testing.B) {
				b.SetBytes(int64(length))
				runBenchmarkWriteToBuffer(b, int64(length))
			})
			b.Run("MarshalBinary", func(b *testing.B) {
				b.SetBytes(int64(length))
				runBenchmarkMarshalBinaryWrite(b, int64(length))
			})
		})
	}
}

func runBenchmarkMarshalBinaryWrite(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: &peerConnMsgWriterBuffer{},
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		//b.StopTimer()
		writer.writeBuffer.Reset()
		//b.StartTimer()
		writer.writeBuffer.Write(msg.MustMarshalBinary())
	}
}
