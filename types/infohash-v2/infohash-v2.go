package infohash_v2

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"unsafe"

	"github.com/multiformats/go-multihash"

	"github.com/anacrolix/torrent/types/infohash"
)

const Size = sha256.Size

// 32-byte SHA2-256 hash. See BEP 52.
type T [Size]byte

var _ fmt.Formatter = (*T)(nil)

func (t *T) Format(f fmt.State, c rune) {
	// TODO: I can't figure out a nice way to just override the 'x' rune, since it's meaningless
	// with the "default" 'v', or .String() already returning the hex.
	f.Write([]byte(t.HexString()))
}

func (t *T) Bytes() []byte {
	return t[:]
}

func (t *T) AsString() string {
	return string(t[:])
}

func (t *T) String() string {
	return t.HexString()
}

func (t *T) HexString() string {
	return fmt.Sprintf("%x", t[:])
}

func (t *T) FromHexString(s string) (err error) {
	if len(s) != 2*Size {
		err = fmt.Errorf("hash hex string has bad length: %d", len(s))
		return
	}
	n, err := hex.Decode(t[:], []byte(s))
	if err != nil {
		return
	}
	if n != Size {
		panic(n)
	}
	return
}

// Truncates the hash to 20 bytes for use in auxiliary interfaces, like DHT and trackers.
func (t *T) ToShort() (short *infohash.T) {
	return (*infohash.T)(unsafe.Pointer(t))
}

var (
	_ encoding.TextUnmarshaler = (*T)(nil)
	_ encoding.TextMarshaler   = T{}
)

func (t *T) UnmarshalText(b []byte) error {
	return t.FromHexString(string(b))
}

func (t T) MarshalText() (text []byte, err error) {
	return []byte(t.HexString()), nil
}

func FromHexString(s string) (h T) {
	err := h.FromHexString(s)
	if err != nil {
		panic(err)
	}
	return
}

func HashBytes(b []byte) (ret T) {
	hasher := sha256.New()
	hasher.Write(b)
	copy(ret[:], hasher.Sum(nil))
	return
}

func ToMultihash(t T) multihash.Multihash {
	b, _ := multihash.Encode(t[:], multihash.SHA2_256)
	return b
}
