package dht

import (
	"crypto/sha1"
)

func HashTuple(bs ...[]byte) (ret [20]byte) {
	h := sha1.New()
	for _, b := range bs {
		h.Reset()
		h.Write(ret[:])
		h.Write(b)
		h.Sum(ret[:0])
	}
	return
}
