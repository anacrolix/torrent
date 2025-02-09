package krpc

import (
	"encoding/binary"
	"math/rand/v2"
	"time"
)

func TimestampTransactionID() string {
	var b [binary.MaxVarintLen64]byte
	_ = binary.PutUvarint(b[:], rand.Uint64())
	v2 := b[:2]
	n := binary.PutVarint(b[:], time.Now().UnixNano())
	b[2] = v2[0]
	b[3] = v2[1]
	return string(b[:n])
}
