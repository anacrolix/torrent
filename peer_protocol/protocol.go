package peer_protocol

type (
	MessageType byte
	Integer     uint32
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

type Message struct {
	KeepAlive bool
	Type      MessageType
	Bitfield  []bool
	Piece     []byte
}
