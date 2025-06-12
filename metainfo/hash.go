package metainfo

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

const HashSize = 20

// 20-byte SHA1 hash used for info and pieces.
type Hash [HashSize]byte

func (h Hash) Bytes() []byte {
	return h[:]
}

func (h Hash) String() string {
	return fmt.Sprintf("%x", h[:])
}

func fromHexString(s string) (h Hash, err error) {
	if len(s) != 2*HashSize {
		return h, fmt.Errorf("hash hex string has bad length: %d", len(s))
	}

	n, err := hex.Decode(h[:], []byte(s))
	if err != nil {
		return h, err
	}

	if n != HashSize {
		return h, fmt.Errorf("hash size to short %d != %d", n, HashSize)
	}

	return h, nil
}

func NewHashFromHex(s string) (h Hash) {
	h, err := fromHexString(s)
	if err != nil {
		panic(err)
	}
	return h
}

func NewHashFromBytes(b []byte) (ret Hash) {
	hasher := sha1.New()
	hasher.Write(b)
	copy(ret[:], hasher.Sum(nil))
	return
}
