package btprotocol

import (
	"testing"

	"github.com/james-lawrence/torrent/mse"
	"github.com/stretchr/testify/require"
)

func TestEncryptionHandshakeRoundTrip(t *testing.T) {
	c1, c2 := newConn()
	key := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 0}
	keys := func(callback func(skey []byte) (more bool)) {
		callback(key)
	}
	p1 := EncryptionHandshake{
		Keys:           keys,
		CryptoSelector: mse.DefaultCryptoSelector,
	}
	p2 := EncryptionHandshake{
		Keys:           keys,
		CryptoSelector: mse.DefaultCryptoSelector,
	}

	p2done := make(chan struct{})
	go func() {
		defer close(p2done)
		rw, buffered, err := p2.Incoming(c2)
		require.NoError(t, err)
		require.Equal(t, rw, buffered)
	}()

	rw, err := p1.Outgoing(c1, key, mse.CryptoMethodRC4)
	<-p2done

	require.NoError(t, err)
	require.NotEqual(t, rw, c1)
}

func TestEncryptionIncomingUnencryptedHandshakeRoundTrip(t *testing.T) {
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
	p2e := EncryptionHandshake{}

	p2done := make(chan struct{})
	go func() {
		defer close(p2done)
		rw, buffered, err := p2e.Incoming(c2)
		require.Error(t, err)
		require.NotEqual(t, rw, buffered)
		bits, p1info, err := p2.Incoming(buffered)
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
