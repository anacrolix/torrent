package torrent

import (
	"golang.org/x/exp/constraints"
)

// a/b rounding up
func intCeilDiv[T constraints.Unsigned](a, b T) T {
	// This still sux for negative numbers due to truncating division. But I don't know that we need
	// or that ceil division makes sense for negative numbers.
	return (a + b - 1) / b
}

func roundToNextMultiple[T constraints.Unsigned](x, multiple T) T {
	return intCeilDiv(x, multiple) * multiple
}
