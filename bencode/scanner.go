package bencode

import (
	"errors"
	"io"
)

// Implements io.ByteScanner over io.Reader, for use in Decoder, to ensure
// that as little as the undecoded input Reader is consumed as possible.
type scanner struct {
	io.Reader
	b      [1]byte // Buffer for ReadByte
	unread bool    // True if b has been unread, and so should be returned next
}

var errAlreadyUnreadByte = errors.New("byte already unread")

func (me *scanner) ReadByte() (byte, error) {
	if me.unread {
		me.unread = false
	} else if n, err := me.Read(me.b[:]); n != 1 {
		return me.b[0], err
	}
	return me.b[0], nil
}

func (me *scanner) UnreadByte() (ret error) {
	if me.unread {
		return errAlreadyUnreadByte
	}
	me.unread = true
	return
}
