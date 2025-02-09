package dht

import (
	"encoding/hex"
	"math"
	"math/big"
)

type Int160 struct {
	bits [20]uint8
}

func (me Int160) String() string {
	return hex.EncodeToString(me.bits[:])
}

func (me *Int160) AsByteArray() [20]byte {
	return me.bits
}

func (me *Int160) ByteString() string {
	return string(me.bits[:])
}

func (me *Int160) BitLen() int {
	var a big.Int
	a.SetBytes(me.bits[:])
	return a.BitLen()
}

func (me *Int160) SetBytes(b []byte) {
	n := copy(me.bits[:], b)
	if n != 20 {
		panic(n)
	}
}

func (me Int160) Bytes() []byte {
	return me.bits[:]
}

func (l Int160) Cmp(r Int160) int {
	for i := range l.bits {
		if l.bits[i] < r.bits[i] {
			return -1
		} else if l.bits[i] > r.bits[i] {
			return 1
		}
	}
	return 0
}

func (me *Int160) SetMax() {
	for i := range me.bits {
		me.bits[i] = math.MaxUint8
	}
}

func (me *Int160) Xor(a, b *Int160) {
	for i := range me.bits {
		me.bits[i] = a.bits[i] ^ b.bits[i]
	}
}

func (me *Int160) IsZero() bool {
	for _, b := range me.bits {
		if b != 0 {
			return false
		}
	}
	return true
}

func Int160FromByteArray(b [20]byte) (ret Int160) {
	ret.SetBytes(b[:])
	return
}

func distance(a, b Int160) (ret Int160) {
	ret.Xor(&a, &b)
	return
}
