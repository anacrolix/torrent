package torrent

import (
	"golang.org/x/exp/constraints"
)

func intCeilDiv[T constraints.Unsigned](a, b T) T {
	// This still sux for negative numbers due to truncating division. But I don't know that we need
	// or that ceil division makes sense for negative numbers.
	return (a + b - 1) / b
}
