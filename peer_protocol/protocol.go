package peer_protocol

import "strconv"

const (
	Protocol = "\x13BitTorrent protocol"
)

type (
	MessageType byte
)

// Hopefully uncaught panics format using this so we don't just see a pair of
// unhelpful uintptrs.
func (me MessageType) String() string {
	return strconv.FormatInt(int64(me), 10)
}

const (
	Choke         MessageType = iota
	Unchoke                   // 1
	Interested                // 2
	NotInterested             // 3
	Have                      // 4
	Bitfield                  // 5
	Request                   // 6
	Piece                     // 7
	Cancel                    // 8
	Port                      // 9

	// BEP 6
	Suggest     = 0xd  // 13
	HaveAll     = 0xe  // 14
	HaveNone    = 0xf  // 15
	Reject      = 0x10 // 16
	AllowedFast = 0x11 // 17

	Extended = 20

	HandshakeExtendedID = 0

	RequestMetadataExtensionMsgType = 0
	DataMetadataExtensionMsgType    = 1
	RejectMetadataExtensionMsgType  = 2
)
