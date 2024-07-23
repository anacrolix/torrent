package bencode

import (
	"errors"
	"io"
)

// Implements io.ByteScanner over io.Reader, for use in Decoder, to ensure
// that as little as the undecoded input Reader is consumed as possible.
type scanner struct {
	r      io.Reader
	b      [1]byte // Buffer for ReadByte
	unread bool    // True if b has been unread, and so should be returned next
}

func (me *scanner) Read(b []byte) (int, error) {
	return me.r.Read(b)
}

func (me *scanner) ReadByte() (byte, error) {
	if me.unread {
		me.unread = false
		return me.b[0], nil
	}
	for {
		n, err := me.r.Read(me.b[:])
		switch n {
		case 0:
			// io.Reader.Read says to try again if there's no error and no bytes returned. We can't
			// signal that the caller should do this method's interface.
			if err != nil {
				return 0, err
			}
			panic(err)
		case 1:
			// There's no way to signal that the byte is valid unless error is nil.
			return me.b[0], nil
		default:
			if err != nil {
				// I can't see why Read would return more bytes than expected, but the error should
				// tell us why.
				panic(err)
			}
			panic(n)
		}
	}
}

func (me *scanner) UnreadByte() error {
	if me.unread {
		return errors.New("byte already unread")
	}
	me.unread = true
	return nil
}
