package storage

import (
	"io"
)

var zeroes [4 << 10]byte

func writeZeroes(w io.Writer, n int64) (written int64, err error) {
	for n > 0 {
		var w1 int
		w1, err = w.Write(zeroes[:min(int64(len(zeroes)), n)][:])
		written += int64(w1)
		if err != nil {
			break
		}
		n -= int64(w1)
	}
	return
}
