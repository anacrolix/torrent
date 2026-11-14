package torrent

import (
	"crypto/sha1"
	"net/netip"
	"testing"

	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/cespare/xxhash/v2"

	"github.com/anacrolix/torrent/smartban"
)

func benchmarkSmartBanRecordBlock[Sum comparable](b *testing.B, hash func([]byte) Sum) {
	var cache smartban.Cache[bannableAddr, RequestIndex, Sum]
	cache.Hash = hash
	cache.Init()
	var data [defaultChunkSize]byte
	var addr netip.Addr
	b.SetBytes(int64(len(data)))
	for i := range iter.N(b.N) {
		cache.RecordBlock(addr, RequestIndex(i), data[:])
	}
}

func BenchmarkSmartBanRecordBlock(b *testing.B) {
	b.Run("xxHash", func(b *testing.B) {
		var salt [8]byte
		benchmarkSmartBanRecordBlock(b, func(block []byte) uint64 {
			// xxHash is not cryptographic, and so we're salting it so attackers can't know a priori
			// where block data collisions are. Keep the digest on the stack to avoid an allocation.
			var d xxhash.Digest
			d.Reset()
			d.Write(salt[:])
			d.Write(block)
			return d.Sum64()
		})
	})
	b.Run("Sha1", func(b *testing.B) {
		benchmarkSmartBanRecordBlock(b, sha1.Sum)
	})
}
