// Package zeroio provides helpers for writing and scanning runs of zero bytes, sharing a single
// zero-filled buffer.
package zeroio

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/bits"
)

// A shared, permanently-zero buffer for WriteZeroes and FirstNonZero.
var zeroes [4 << 10]byte

// WriteZeroes writes n zero bytes to w. It reuses a shared buffer, so unlike io.CopyN with a zero
// reader it doesn't allocate or re-zero a buffer per call.
func WriteZeroes(w io.Writer, n int64) (written int64, err error) {
	for written < n {
		var w1 int
		w1, err = w.Write(zeroes[:min(int64(len(zeroes)), n-written)])
		written += int64(w1)
		if err != nil {
			return
		}
	}
	return
}

// FirstNonZero returns the index of the first non-zero byte in p, or len(p) if every byte is zero.
//
// Whole zeroes-sized chunks are skipped with bytes.Equal, which lowers to the SIMD memequal assembly
// and clears all-zero data at memory-bandwidth speed; the first non-zero chunk (and any tail) is then
// scanned a word at a time. LittleEndian.Uint64 places p[i] in the low byte on every platform, so
// TrailingZeros64/8 is the first non-zero byte index regardless of machine endianness.
func FirstNonZero(p []byte) int {
	const chunk = len(zeroes)
	i := 0
	for ; i+chunk <= len(p); i += chunk {
		if !bytes.Equal(p[i:i+chunk], zeroes[:]) {
			break
		}
	}
	for ; i+8 <= len(p); i += 8 {
		if w := binary.LittleEndian.Uint64(p[i:]); w != 0 {
			return i + bits.TrailingZeros64(w)/8
		}
	}
	for ; i < len(p); i++ {
		if p[i] != 0 {
			return i
		}
	}
	return i
}
