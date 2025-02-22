package dht

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/anacrolix/log"
	"github.com/anacrolix/torrent/bencode"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/dht/v2/bep44"
)

func TestPutGet(t *testing.T) {
	require := require.New(t)

	l := log.Default.WithNames(t.Name())
	s1 := newServer(t, l.WithNames("s1"))
	s2 := newServer(t, l.WithNames("s2"))

	s2Addr := NewAddr(s2.Addr())

	immuItem, err := bep44.NewItem("Hello World! immu", nil, 1, 1, nil)
	require.NoError(err)

	// send get request to s2, we need a write token to put data
	qr := s1.Get(context.TODO(), s2Addr, immuItem.Target(), nil, QueryRateLimiting{})
	require.NoError(qr.ToError())
	require.NotNil(qr.Reply.R)
	require.NotNil(qr.Reply.R.Token)

	// send put request to s2
	qr = s1.Put(context.TODO(), s2Addr, immuItem.ToPut(), *qr.Reply.R.Token, QueryRateLimiting{})
	require.NoError(qr.ToError())

	qr = s1.Get(context.TODO(), s2Addr, immuItem.Target(), nil, QueryRateLimiting{})
	require.NoError(qr.ToError())
	var vStr string // heueahea
	require.NoError(bencode.Unmarshal(qr.Reply.R.V, &vStr))
	require.Equal("Hello World! immu", vStr)

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(err)

	mutItem, err := bep44.NewItem("Hello World!", []byte("s1"), 1, 1, priv)
	require.NoError(err)

	// send get request to s2, we need a write token to put data
	qr = s1.Get(context.TODO(), s2Addr, mutItem.Target(), nil, QueryRateLimiting{})
	require.NoError(qr.ToError())
	require.NotNil(qr.Reply.R)

	mutToken := qr.Reply.R.Token
	require.NotNil(mutToken)

	// send put request to s2
	qr = s1.Put(context.TODO(), s2Addr, mutItem.ToPut(), *mutToken, QueryRateLimiting{})
	require.NoError(qr.ToError())

	qr = s1.Get(context.TODO(), s2Addr, mutItem.Target(), nil, QueryRateLimiting{})
	require.NoError(qr.ToError())
	require.NoError(bencode.Unmarshal(qr.Reply.R.V, &vStr))
	require.Equal("Hello World!", vStr)

	ii, err := s2.store.Get(immuItem.Target())
	require.NoError(err)
	require.Equal("Hello World! immu", ii.V)

	mi, err := s2.store.Get(mutItem.Target())
	require.NoError(err)
	require.Equal("Hello World!", mi.V)

	// change mutable item
	ok := mutItem.Modify("Bye World!", priv)
	require.True(ok)
	qr = s1.Put(context.TODO(), s2Addr, mutItem.ToPut(), *mutToken, QueryRateLimiting{})
	require.NoError(qr.ToError())

	mi, err = s2.store.Get(mutItem.Target())
	require.NoError(err)
	require.Equal("Bye World!", mi.V)

	qr = s1.Get(context.TODO(), s2Addr, mutItem.Target(), nil, QueryRateLimiting{})
	require.NoError(qr.ToError())
	require.NoError(bencode.Unmarshal(qr.Reply.R.V, &vStr))
	require.Equal("Bye World!", vStr)

	seqPtr := new(int64)
	*seqPtr = 3
	qr = s1.Get(context.TODO(), s2Addr, mutItem.Target(), seqPtr, QueryRateLimiting{})
	require.NoError(qr.ToError())
	require.Nil(qr.Reply.R.V)
	require.Equal(int64(2), *qr.Reply.R.Seq)
}

func newServer(t *testing.T, l log.Logger) *Server {
	cfg := NewDefaultServerConfig()
	cfg.WaitToReply = true

	cfg.Conn = mustListen("localhost:0")
	cfg.Logger = l
	s, err := NewServer(cfg)
	if err != nil {
		panic(err)
	}

	t.Cleanup(func() {
		s.Close()
	})

	return s
}
