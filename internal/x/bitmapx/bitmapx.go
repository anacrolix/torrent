package bitmapx

import (
	"math/rand/v2"

	"github.com/RoaringBitmap/roaring/v2"
)

// Bools convert to an array of bools
func Bools(n int, m *roaring.Bitmap) (bf []bool) {
	bf = make([]bool, n)

	for i := m.Iterator(); i.HasNext() && int(i.PeekNext()) < len(bf); {
		bf[i.Next()] = true
	}

	return bf
}

// Lazy ...
func Lazy(m *roaring.Bitmap) *roaring.Bitmap {
	if m != nil {
		return m
	}

	return roaring.New()
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
func AndNot(l *roaring.Bitmap, rs ...*roaring.Bitmap) (dup *roaring.Bitmap) {
	dup = l.Clone()
	for _, r := range rs {
		dup.AndNot(r)
	}
	return dup
}

func Range(min, max uint64) *roaring.Bitmap {
	m := roaring.New()
	m.AddRange(min, max)
	return m
}

func Zero(max uint64) *roaring.Bitmap {
	m := roaring.New()
	m.AddRange(0, max)
	m.Clear()
	return m
}

func Fill(max uint64) *roaring.Bitmap {
	return Range(0, max)
}

func Random(max uint32, bits int, src rand.Source) *roaring.Bitmap {
	r := rand.New(src)
	m := roaring.New()

	for range bits {
		v := r.Uint32N(max)
		m.Add(v)
	}

	return m
}
