package peer_protocol

import (
	"unsafe"
)

const (
	Protocol = "\x13BitTorrent protocol"
)

func protocolBytes() []byte {
	return unsafe.Slice(unsafe.StringData(Protocol), len(Protocol))
}

type MessageType byte

// golang.org/x/tools/cmd/stringer
//go:generate stringer -type=MessageType

func (mt MessageType) FastExtension() bool {
	return mt >= Suggest && mt <= AllowedFast
}

func (mt *MessageType) UnmarshalBinary(b []byte) error {
	*mt = MessageType(b[0])
	return nil
}

const (
	// BEP 3
	Choke         MessageType = 0
	Unchoke       MessageType = 1
	Interested    MessageType = 2
	NotInterested MessageType = 3
	Have          MessageType = 4
	Bitfield      MessageType = 5
	Request       MessageType = 6
	Piece         MessageType = 7
	Cancel        MessageType = 8

	// BEP 5
	Port MessageType = 9

	// BEP 6 - Fast extension
	Suggest     MessageType = 0x0d // 13
	HaveAll     MessageType = 0x0e // 14
	HaveNone    MessageType = 0x0f // 15
	Reject      MessageType = 0x10 // 16
	AllowedFast MessageType = 0x11 // 17

	// BEP 10
	Extended MessageType = 20

	// BEP 52
	HashRequest MessageType = 21
	Hashes      MessageType = 22
	HashReject  MessageType = 23
)

const (
	HandshakeExtendedID = 0

	RequestMetadataExtensionMsgType ExtendedMetadataRequestMsgType = 0
	DataMetadataExtensionMsgType    ExtendedMetadataRequestMsgType = 1
	RejectMetadataExtensionMsgType  ExtendedMetadataRequestMsgType = 2
)
