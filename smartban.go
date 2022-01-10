package torrent

import (
	"bytes"
	"crypto/sha1"

	"github.com/anacrolix/torrent/smartban"
)

type banPrefix = string

type smartBanCache = smartban.Cache[banPrefix, RequestIndex, [sha1.Size]byte]

type blockCheckingWriter struct {
	cache        *smartBanCache
	requestIndex RequestIndex
	// Peers that didn't match blocks written now.
	badPeers    map[banPrefix]struct{}
	blockBuffer bytes.Buffer
	chunkSize   int
}

func (me *blockCheckingWriter) checkBlock() {
	b := me.blockBuffer.Next(me.chunkSize)
	for _, peer := range me.cache.CheckBlock(me.requestIndex, b) {
		me.badPeers[peer] = struct{}{}
	}
	me.requestIndex++
}

func (me *blockCheckingWriter) checkFullBlocks() {
	for me.blockBuffer.Len() >= me.chunkSize {
		me.checkBlock()
	}
}

func (me *blockCheckingWriter) Write(b []byte) (int, error) {
	n, err := me.blockBuffer.Write(b)
	if err != nil {
		// bytes.Buffer.Write should never fail.
		panic(err)
	}
	me.checkFullBlocks()
	return n, err
}

// Check any remaining block data. Terminal pieces or piece sizes that don't divide into the chunk
// size cleanly may leave fragments that should be checked.
func (me *blockCheckingWriter) Flush() {
	for me.blockBuffer.Len() != 0 {
		me.checkBlock()
	}
}
