package peer_protocol

type (
	MessageType byte
	Integer     uint32
)

const (
	Protocol = "\x13BitTorrent protocol"
)

const (
	Choke MessageType = iota
	Unchoke
	Interested
	NotInterested
	Have
	Bitfield
	RequestType
	Piece
	Cancel
)

type Request struct {
	Index, Begin, Length Integer
}

type Message interface {
	Encode() []byte
}

type Bytes []byte

func (b Bytes) Encode() []byte {
	return b
}
