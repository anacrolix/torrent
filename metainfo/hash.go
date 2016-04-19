package metainfo

import "fmt"

// 20-byte SHA1 hash used for info and pieces.
type Hash [20]byte

func (h Hash) Bytes() []byte {
	return h[:]
}

func (h *Hash) AsString() string {
	return string(h[:])
}

func (h Hash) HexString() string {
	return fmt.Sprintf("%x", h[:])
}
