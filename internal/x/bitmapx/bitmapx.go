package bitmapx

import (
	"math/rand/v2"

	"github.com/RoaringBitmap/roaring/v2"
	"golang.org/x/exp/constraints"
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
	dup = Lazy(l).Clone()
	for _, r := range rs {
		dup.AndNot(Lazy(r))
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

func Fill[T constraints.Integer](max T) *roaring.Bitmap {
	return Range(0, uint64(max))
}

func RandomFromSource(max uint64, bits uint64, src rand.Source) *roaring.Bitmap {
	m := roaring.New()
	m.AddMany(sample(src, uint32(max), uint32(bits)))

	return m
}

func Random(max uint64, bits uint64) *roaring.Bitmap {
	return RandomFromSource(max, bits, rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

func sample[T constraints.Integer](src rand.Source, n T, k T) []T {
	r := rand.New(src)

	if k > n {
		k = n // If k is larger than the stream, return the entire stream
	}

	stream := make([]T, n)
	for i := range int(n) {
		stream[i] = T(i)
	}

	reservoir := make([]T, k)

	// Fill the reservoir with the first k elements
	for i := range int(k) {
		reservoir[i] = stream[i]
	}

	// Process the remaining elements in the stream
	for i := k; i < T(len(stream)); i++ {
		j := r.IntN(int(i + 1)) // Generate a random number between 0 and i (inclusive)
		if T(j) < k {
			reservoir[j] = stream[i]
		}
	}

	return reservoir
}
