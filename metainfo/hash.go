package metainfo

import (
	"crypto/sha1"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"unsafe"
)

const (
	HashSize = sha1.Size

	// HashEncodedLen is the size of the hex encoded hash.
	// Equivalent to hex.EncodedLen(HashSize)
	HashEncodedLen = 2*HashSize
)

// 20-byte SHA1 hash used for info and pieces.
type Hash [HashSize]byte

var (
	_ fmt.Formatter = (*Hash)(nil)
	_ fmt.Stringer = (*Hash)(nil)
	_ encoding.TextUnmarshaler = (*Hash)(nil)
	_ encoding.TextMarshaler   = (*Hash)(nil)
)

func (h *Hash) Format(f fmt.State, c rune) {
	bs := h.hexBytes()
	switch c {
	case 's', 'v', 'x': break
	case 'X':
		for i := 0; i < len(bs); i += 1 {
			if bs[i] >= 'a' {
				bs[i] -= 'a' - 'A'
			}
		}
	default:
		// TODO: handle other verbs and flags?
	}
	f.Write(bs)
}

func (h *Hash) String() string {
	return h.HexString()
}

func (h *Hash) UnmarshalText(b []byte) error {
	if len(b) != HashEncodedLen {
		return errors.New(hex.ErrLength.Error() + ": " + strconv.Itoa(len(b)))
	}
	return h.decodeBytesFrom(b)
}

func (h *Hash) MarshalText() (text []byte, err error) {
	return h.hexBytes(), nil
}

// Bytes copies the Hash into a new byte slice
func (h *Hash) Bytes() []byte {
	h2 := *h
	return h2[:]
}

// AsString copies the *Hash without hex encoding into a string of length HashSize
func (h *Hash) AsString() string {
	return string(h[:])
}

// HexString hex encodes *Hash to a new string
func (h *Hash) HexString() string {
	bs := h.hexBytes()
	// We created a new byte slice which we own. Thus, we can safely transfer
	// ownership of its data to a string without additional allocations
	// using the same technique as strings.Builder's String() method
	return *(*string)(unsafe.Pointer(&bs))
}

func (h *Hash) FromHexString(s string) (err error) {
	if len(s) != HashEncodedLen {
		return errors.New(hex.ErrLength.Error() + ": " + strconv.Itoa(len(s)))
	}
	// Strings are immutable in Go, so we can C-style cast the string to a byte slice
	// without worrying about external modifications of the data.
	// However, in order to uphold this immutability we must guarantee that the resulting slice
	// is READ-ONLY. This is easy to do provided that the slice does not escape.
	// Go's lack of immutability prevents it from providing these types of optimizations for us.
	bs := (*[HashEncodedLen]byte)(unsafe.Pointer((*reflect.StringHeader)(unsafe.Pointer(&s)).Data))[:]
	return h.decodeBytesFrom(bs)
}

// NewHashFromHex creates a Hash from hex-encoded string of length HashEncodedLen
// Panics on error.
func NewHashFromHex(s string) (h Hash) {
	if err := h.FromHexString(s); err != nil {
		panic(err)
	}
	return
}

// HashBytes returns the SHA-1 checksum of data as a Hash
func HashBytes(data []byte) (ret Hash) {
	return sha1.Sum(data)
}


// hex.Decode src []byte into *Hash. The src []byte will not be mutated.
// Caller should ensure that src is at least HashEncodedLen long.
func (h *Hash) decodeBytesFrom(src []byte) (err error) {
	_, err = hex.Decode(h[:], src)
	return
}

// hex.Encode *Hash into a new array of length HashEncodedLen
func (h *Hash) hexEncodeToArray() (dst [HashEncodedLen]byte) {
	hex.Encode(dst[:], h[:])
	return
}

// hex.Encode *Hash into a new slice
func (h *Hash) hexBytes() []byte {
	dst := h.hexEncodeToArray()
	return dst[:]
}
