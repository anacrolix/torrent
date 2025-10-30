package types

import (
	"encoding/json"
	"fmt"
)

// Peer client ID.
type PeerID [20]byte

//var _ slog.LogValuer = PeerID{}

func (me PeerID) String() string {
	return fmt.Sprintf("%+q", me[:])
}

//
//func (me PeerID) LogValue() slog.Value {
//	return slog.StringValue(fmt.Sprintf("%+q", me[:]))
//}

func (me PeerID) MarshalJSON() ([]byte, error) {
	return json.Marshal(me.String())
}

// // Pretty prints the ID as hex, except parts that adhere to the PeerInfo ID
// // Conventions of BEP 20.
// func (me PeerID) String() string {
// 	// if me[0] == '-' && me[7] == '-' {
// 	// 	return string(me[:8]) + hex.EncodeToString(me[8:])
// 	// }
// 	// return hex.EncodeToString(me[:])
// 	return fmt.Sprintf("%+q", me[:])
// }
