package dht

import (
	"fmt"

	"github.com/anacrolix/torrent/util"
)

// The unmarshalled KRPC dict message.
type Msg struct {
	Q string `bencode:"q,omitempty"`
	A *struct {
		ID       string `bencode:"id"`
		InfoHash string `bencode:"info_hash"`
		Target   string `bencode:"target"`
	} `bencode:"a,omitempty"`
	T string     `bencode:"t"`
	Y string     `bencode:"y"`
	R *Return    `bencode:"r,omitempty"`
	E *KRPCError `bencode:"e,omitempty"`
}

type Return struct {
	ID     string              `bencode:"id"`
	Nodes  CompactIPv4NodeInfo `bencode:"nodes,omitempty"`
	Token  string              `bencode:"token"`
	Values []util.CompactPeer  `bencode:"values,omitempty"`
}

var _ fmt.Stringer = Msg{}

func (m Msg) String() string {
	return fmt.Sprintf("%#v", m)
}

// The node ID of the source of this Msg.
func (m Msg) SenderID() string {
	switch m.Y {
	case "q":
		return m.A.ID
	case "r":
		return m.R.ID
	}
	return ""
}

func (m Msg) Error() *KRPCError {
	if m.Y != "e" {
		return nil
	}
	return m.E
}
