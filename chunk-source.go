package torrent

import (
	"context"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/missinggo/v2/panicif"
)

// Something a Torrent receives chunks from: a PeerConn or a WebSeed. This is what Torrent asks of
// either without knowing which it is, to credit or blame it for the pieces its chunks went into.
type chunkProvider interface {
	// The stats and download bookkeeping for the chunks received from it.
	source() *chunkSource
	trust() connectionTrust
	// Rebuke it for a piece that failed its hash.
	providedBadData()
	// Notify that its requests should be updated for the provided reason.
	onNeedUpdateRequests(reason updateRequestReason)
}

// The state kept for anything chunks are received from, embedded in PeerConn and WebSeed. It calls
// nothing on the type that embeds it.
type chunkSource struct {
	// First to ensure 64-bit alignment for atomics. See #262.
	_stats ConnStats
	// Which of a Torrent's and Client's per-type ConnStats this counts toward.
	kind chunkSourceKind

	lastUsefulChunkReceived time.Time

	lastStartedExpectingToReceiveChunks time.Time
	cumulativeExpectedToReceiveChunks   time.Duration
	// Pieces we've accepted chunks for from this source.
	peerTouchedPieces map[pieceIndex]struct{}
}

// The zero value is a PeerConn.
type chunkSourceKind uint8

const (
	chunkSourcePeerConn chunkSourceKind = iota
	chunkSourceWebSeed
)

func (k chunkSourceKind) connStats(all *AllConnStats) *ConnStats {
	if k == chunkSourceWebSeed {
		return &all.WebSeeds
	}
	return &all.PeerConns
}

// The stats every chunk source has. Requires the Client lock.
func (cs *chunkSource) baseStats() (ret PeerStats) {
	ret.ConnStats = cs._stats.Copy()
	ret.DownloadRate = cs.downloadRate()
	return
}

// Records whether we're owed chunks, to time how long the source is expected to be sending. Each
// type calls this when what it's waiting for changes.
func (cs *chunkSource) setExpectingChunks(expecting bool) {
	if expecting {
		if cs.lastStartedExpectingToReceiveChunks.IsZero() {
			cs.lastStartedExpectingToReceiveChunks = time.Now()
		}
	} else {
		if !cs.lastStartedExpectingToReceiveChunks.IsZero() {
			cs.cumulativeExpectedToReceiveChunks += time.Since(cs.lastStartedExpectingToReceiveChunks)
			cs.lastStartedExpectingToReceiveChunks = time.Time{}
		}
	}
}

func (cs *chunkSource) totalExpectingTime() (ret time.Duration) {
	ret = cs.cumulativeExpectedToReceiveChunks
	if !cs.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(cs.lastStartedExpectingToReceiveChunks)
	}
	return
}

func (cs *chunkSource) downloadRate() float64 {
	num := cs._stats.BytesReadUsefulData.Int64()
	if num == 0 {
		return 0
	}
	return float64(num) / cs.totalExpectingTime().Seconds()
}

func (cs *chunkSource) netGoodPiecesDirtied() int64 {
	return cs._stats.PiecesDirtiedGood.Int64() - cs._stats.PiecesDirtiedBad.Int64()
}

// The status lines every chunk source ends with.
func (cs *chunkSource) writeStatus(w io.Writer) {
	fmt.Fprintf(w,
		"%d pieces touched, good chunks: %v/%v, dr: %.1f KiB/s\n",
		len(cs.peerTouchedPieces),
		&cs._stats.ChunksReadUseful,
		&cs._stats.ChunksRead,
		cs.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "\n")
}

// Applies f to all ConnStats that include this source: its own, and those of the Torrent and
// Client it belongs to. t is nil for a PeerConn that hasn't completed its handshake, after which
// it's expected to reconcile the differences.
func (cs *chunkSource) modifyConnStats(t *Torrent, cl *Client, f func(*ConnStats)) {
	f(&cs._stats)
	for stats := range cs.upstreamConnStats(t, cl) {
		f(stats)
	}
}

// Yields relevant upstream ConnStats. Skips Torrent if it isn't set.
func (cs *chunkSource) upstreamConnStats(t *Torrent, cl *Client) iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		if t != nil {
			for s := range cs.relevantConnStats(&t.connStats) {
				if !yield(s) {
					return
				}
			}
		}
		cs.relevantConnStats(&cl.connStats)(yield)
	}
}

// Iterates base and per-type ConnStats from all.
func (cs *chunkSource) relevantConnStats(all *AllConnStats) iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		if !yield(&all.ConnStats) {
			return
		}
		yield(cs.kind.connStats(all))
	}
}

// Whether something is closed, with a context that's cancelled when it is. Embedded in PeerConn
// and WebSeed.
type closedState struct {
	closed          chansync.SetOnce
	closedCtx       context.Context
	closedCtxCancel context.CancelFunc
}

func (me *closedState) initClosedCtx(parent context.Context) {
	panicif.NotNil(me.closedCtx)
	me.closedCtx, me.closedCtxCancel = context.WithCancel(parent)
}

// Marks as closed and cancels the context. Returns false if it was already closed.
func (me *closedState) beginClose() bool {
	if !me.closed.Set() {
		return false
	}
	// Not set until Torrent is known.
	if me.closedCtx != nil {
		me.closedCtxCancel()
	}
	return true
}
