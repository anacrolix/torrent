package utHolepunch

import (
	"fmt"
)

type ErrCode uint32

var _ error = ErrCode(0)

const (
	NoSuchPeer ErrCode = iota + 1
	NotConnected
	NoSupport
	NoSelf
)

func (ec ErrCode) Error() string {
	switch ec {
	case NoSuchPeer:
		return "target endpoint is invalid"
	case NotConnected:
		return "the relaying peer is not connected to the target peer"
	case NoSupport:
		return "the target peer does not support the holepunch extension"
	case NoSelf:
		return "the target endpoint belongs to the relaying peer"
	default:
		return fmt.Sprintf("error code %d", ec)
	}
}
