package bitmapx

type bmap interface {
	IterTyped(func(int) bool) bool
}

func Bools(n int, m bmap) (bf []bool) {
	bf = make([]bool, n)
	m.IterTyped(func(piece int) (again bool) {
		bf[piece] = true
		return true
	})

	return bf
}
