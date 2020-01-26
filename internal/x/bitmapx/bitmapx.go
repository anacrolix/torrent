package bitmapx

import (
	"github.com/RoaringBitmap/roaring"
)

type bmap interface {
	IterTyped(func(int) bool) bool
}

// Bools convert to an array of bools
func Bools(n int, m bmap) (bf []bool) {
	bf = make([]bool, n)
	m.IterTyped(func(piece int) (again bool) {
		bf[piece] = true
		return true
	})

	return bf
}

// Lazy ...
func Lazy(c *roaring.Bitmap) *roaring.Bitmap {
	if c != nil {
		return c
	}

	return roaring.NewBitmap()
}
