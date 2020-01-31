package bitmapx

import (
	"fmt"

	"github.com/RoaringBitmap/roaring"
)

type bmap interface {
	IterTyped(func(int) bool) bool
}

// Bools convert to an array of bools
func Bools(n int, m *roaring.Bitmap) (bf []bool) {
	bf = make([]bool, n)

	for i := m.Iterator(); i.HasNext(); {
		bf[i.Next()] = true
	}

	return bf
}

// Lazy ...
func Lazy(m *roaring.Bitmap) *roaring.Bitmap {
	if m != nil {
		return m
	}

	return roaring.NewBitmap()
}

// Debug print the bitmap
func Debug(m *roaring.Bitmap) string {
	m = Lazy(m)
	v := make([]int, 0, m.GetCardinality())
	iter := m.Iterator()
	for iter.HasNext() {
		idx := int(iter.Next())
		v = append(v, idx)
	}
	return fmt.Sprintf("%+v", v)
}

// Contains returns iff all the bits are set within the bitmap
func Contains(m *roaring.Bitmap, bits ...int) (b bool) {
	m = Lazy(m)
	b = true
	for _, i := range bits {
		b = b && m.ContainsInt(i)
	}
	return b
}

// AndNot returns the combination of the two bitmaps without modifying
func AndNot(l, r *roaring.Bitmap) (dup *roaring.Bitmap) {
	dup = l.Clone()
	dup.AndNot(r)
	return dup

}
