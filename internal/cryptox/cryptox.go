package cryptox

import (
	"crypto/md5"
	"math/rand/v2"
)

func NewChaCha8[T ~[]byte | string](seed T) *rand.ChaCha8 {
	var (
		vector [32]byte
		source = []byte(seed)
	)

	v1 := md5.Sum(source)
	v2 := md5.Sum(append(v1[:], source...))
	copy(vector[:15], v1[:])
	copy(vector[16:], v2[:])

	return rand.NewChaCha8(vector)
}
