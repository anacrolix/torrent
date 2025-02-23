package krpc

import (
	"encoding/binary"
	"math/rand/v2"
	"time"
)

func TimestampTransactionID() string {
	var b [binary.MaxVarintLen64]byte
	binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint16(b[binary.MaxVarintLen64-2:], uint16(rand.Uint64()))
	return string(b[:])
}
