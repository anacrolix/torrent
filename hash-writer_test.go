package torrent

import (
	"fmt"
	"testing"
)

// Benchmarks hashWriter.Write on all-zero buffers: the leading-zero scan path that lets the hasher
// accumulate runs of zeroes instead of hashing them. Reports the scan throughput (B/s).
func BenchmarkHashWriterWriteZeroes(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			buf := make([]byte, size)
			// The buffer is all zeroes, so the writer never materializes a hash and every Write takes
			// the scan-for-non-zero path.
			hw := &hashWriter{hashCache: sha1HashCache}
			b.SetBytes(int64(size))
			for b.Loop() {
				hw.Write(buf)
			}
		})
	}
}
