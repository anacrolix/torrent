package torrent

import (
	"net/netip"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/smartban"
)

type bannableAddr = netip.Addr

// TODO: Should be keyed on weak[Peer].
type smartBanCache = smartban.Cache[bannableAddr, RequestIndex, uint64]

type blockCheckingWriter struct {
	cache        *smartBanCache
	requestIndex RequestIndex
	// Peers that didn't match blocks written now.
	badPeers    map[bannableAddr]struct{}
	chunkBuffer []byte
	bufferUsed  int
}

func (me *blockCheckingWriter) chunkSize() int {
	return len(me.chunkBuffer)
}

func (me *blockCheckingWriter) checkBufferedBlock() {
	panicif.Zero(me.bufferUsed)
	b := me.chunkBuffer[:me.bufferUsed]
	me.bufferUsed = 0
	me.checkBlock(b)
}

func (me *blockCheckingWriter) checkBlock(b []byte) {
	for _, peer := range me.cache.CheckBlock(me.requestIndex, b) {
		g.MakeMapIfNil(&me.badPeers)
		me.badPeers[peer] = struct{}{}
	}
	me.requestIndex++
}

func (me *blockCheckingWriter) finishPartialBlock(b []byte) int {
	if me.bufferUsed == 0 {
		return 0
	}
	panicif.NotEq(len(me.chunkBuffer), me.chunkSize())
	n := copy(me.chunkBuffer[me.bufferUsed:], b)
	me.bufferUsed += n
	if me.bufferUsed >= me.chunkSize() {
		me.checkBufferedBlock()
	}
	return n
}

func (me *blockCheckingWriter) Write(b []byte) (n int, err error) {
	n = me.finishPartialBlock(b)
	b = b[n:]
	if len(b) == 0 {
		return
	}
	for len(b) >= me.chunkSize() {
		me.checkBlock(b[:me.chunkSize()])
		b = b[me.chunkSize():]
		n += me.chunkSize()
	}
	panicif.NotZero(me.bufferUsed)
	me.bufferUsed = copy(me.chunkBuffer, b)
	n += me.bufferUsed
	return n, err
}

// Check any remaining block data. Terminal pieces or piece sizes that don't divide into the chunk
// size cleanly may leave fragments that should be checked.
func (me *blockCheckingWriter) Flush() {
	if me.bufferUsed != 0 {
		me.checkBufferedBlock()
	}
	panicif.NotZero(me.bufferUsed)
}
