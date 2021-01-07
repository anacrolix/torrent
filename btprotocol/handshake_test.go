package btprotocol

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandshakeMessageWriteRead(t *testing.T) {
	out := HandshakeMessage{}
	in := HandshakeMessage{
		Extensions: [8]byte{1},
	}

	buf := bytes.NewBuffer(nil)
	length, err := in.WriteTo(buf)
	require.NoError(t, err)
	require.Equal(t, length, int64(28))

	length, err = out.ReadFrom(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, length, int64(28))
	require.Equal(t, in, out)
}

func TestHandshakeInfoMessageWriteRead(t *testing.T) {
	out := HandshakeInfoMessage{}
	in := HandshakeInfoMessage{
		PeerID: [20]byte{1},
		Hash:   [20]byte{1},
	}

	buf := bytes.NewBuffer(nil)
	length, err := in.WriteTo(buf)
	require.NoError(t, err)
	require.Equal(t, length, int64(40))

	length, err = out.ReadFrom(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, length, int64(40))
	require.Equal(t, in, out)
}

type conn struct {
	io.Reader
	io.Writer
}

func newConn() (conn, conn) {
	p1r, p1w := io.Pipe()
	p2r, p2w := io.Pipe()
	c1 := conn{
		Reader: p1r,
		Writer: p2w,
	}
	c2 := conn{
		Reader: p2r,
		Writer: p1w,
	}
	return c1, c2
}

func TestHandshakeRoundTrip(t *testing.T) {
	c1, c2 := newConn()
	hash := [20]byte{3}

	p1 := Handshake{
		PeerID: [20]byte{1},
		Bits:   [8]byte{1},
	}

	p2 := Handshake{
		PeerID: [20]byte{2},
		Bits:   [8]byte{2},
	}

	p2done := make(chan struct{})
	go func() {
		defer close(p2done)
		bits, p1info, err := p2.Incoming(c2)
		require.NoError(t, err)
		require.Equal(t, bits, p1.Bits)
		require.Equal(t, p1info.Hash, hash)
		require.Equal(t, p1info.PeerID, p1.PeerID)
	}()

	bits, p2info, err := p1.Outgoing(c1, hash)
	<-p2done

	require.NoError(t, err)
	require.Equal(t, bits, p2.Bits)
	require.Equal(t, p2info.Hash, hash)
	require.Equal(t, p2info.PeerID, p2.PeerID)
}
