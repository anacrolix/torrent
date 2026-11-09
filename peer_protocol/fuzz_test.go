package peer_protocol

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"slices"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x00"))
	f.Add([]byte("\x00\x00\x00\x03\x14\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x07"))
	f.Fuzz(func(t *testing.T, b []byte) {
		t.Logf("%q", b)
		d := Decoder{
			R:         bufio.NewReader(bytes.NewReader(b)),
			MaxLength: 0x100,
		}
		var ms []Message
		for {
			var m Message
			err := d.Decode(&m)
			t.Log(err)
			if err == io.EOF {
				break
			}
			if err == nil {
				qt.Assert(t, qt.Not(qt.Equals(m, Message{})))
				ms = append(ms, m)
				continue
			} else {
				t.Skip(err)
			}
		}
		t.Log(ms)
		var buf bytes.Buffer
		for _, m := range ms {
			buf.Write(m.MustMarshalBinary())
		}
		if len(b) == 0 {
			qt.Assert(t, qt.HasLen(buf.Bytes(), 0))
		} else {
			qt.Assert(t, qt.DeepEquals(buf.Bytes(), b))
		}
	})
}

func FuzzMessageMarshalBinary(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		var m Message
		if err := m.UnmarshalBinary(b); err != nil {
			t.Skip(err)
		}
		b0 := m.MustMarshalBinary()
		qt.Assert(t, qt.DeepEquals(b0, b))
	})
}

// Handshakes are the first bytes read from any incoming or outgoing connection.
func FuzzHandshake(f *testing.F) {
	var b []byte
	b = append(b, Protocol...)
	b = append(b, make([]byte, 8+20+20)...)
	f.Add(b, false)
	f.Add(b, true)
	f.Fuzz(func(t *testing.T, b []byte, knownInfohash bool) {
		var ih *metainfo.Hash
		if knownInfohash {
			ih = &metainfo.Hash{}
		}
		sock := struct {
			io.Reader
			io.Writer
		}{bytes.NewReader(b), io.Discard}
		res, err := Handshake(context.Background(), sock, ih, [20]byte{}, PeerExtensionBits{})
		if err != nil {
			return
		}
		qt.Assert(t, qt.DeepEquals(b[len(Protocol):68], slices.Concat(
			res.PeerExtensionBits[:], res.Hash[:], res.PeerID[:])))
	})
}

func FuzzExtendedHandshakeMessage(f *testing.F) {
	f.Add([]byte("d1:md11:ut_metadatai1e6:ut_pexi2ee13:metadata_sizei1024e1:pi6881e4:reqqi250e1:v7:test1.04:yourip4:\x01\x02\x03\x044:ipv616:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01e"))
	f.Fuzz(func(t *testing.T, b []byte) {
		var m ExtendedHandshakeMessage
		if err := bencode.Unmarshal(b, &m); err != nil {
			return
		}
		_, err := bencode.Marshal(m)
		qt.Assert(t, qt.IsNil(err))
	})
}

func FuzzPexMsg(f *testing.F) {
	f.Add([]byte("d5:added6:\x01\x02\x03\x04\x1a\xe17:added.f1:\x016:added618:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x1a\xe18:added6.f1:\x007:dropped6:\x05\x06\x07\x08\x00\x01e"))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := LoadPexMsg(b)
		if err != nil {
			return
		}
		for _, f := range m.AddedFlags {
			f.Get(PexSupportsUtp)
		}
		_ = m.Message(1).MustMarshalBinary()
	})
}
