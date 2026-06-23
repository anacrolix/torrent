package torrent

import "io"

// A shared, permanently-zero buffer for writeZeroes.
var zeroes [4 << 10]byte

// writeZeroes writes n zero bytes to w. It reuses a shared buffer, so unlike io.CopyN with a zero
// reader it doesn't allocate or re-zero a buffer per call.
func writeZeroes(w io.Writer, n int64) (written int64, err error) {
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
