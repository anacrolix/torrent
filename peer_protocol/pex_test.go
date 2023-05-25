package peer_protocol

import (
	"bufio"
	"bytes"
	"net"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/bencode"
)

func TestUnmarshalPex(t *testing.T) {
	var pem PexMsg
	err := bencode.Unmarshal([]byte("d5:added12:\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0ce"), &pem)
	require.NoError(t, err)
	require.EqualValues(t, 2, len(pem.Added))
	require.EqualValues(t, 1286, pem.Added[0].Port)
	require.EqualValues(t, 0x100*0xb+0xc, pem.Added[1].Port)
}

func TestEmptyPexMsg(t *testing.T) {
	pm := PexMsg{}
	b, err := bencode.Marshal(pm)
	t.Logf("%q", b)
	require.NoError(t, err)
	require.NoError(t, bencode.Unmarshal(b, &pm))
}

func TestMarshalPexMessage(t *testing.T) {
	addr := krpc.NodeAddr{IP: net.IP{127, 0, 0, 1}, Port: 0x55aa}
	f := PexPrefersEncryption | PexOutgoingConn
	pm := new(PexMsg)
	pm.Added = append(pm.Added, addr)
	pm.AddedFlags = append(pm.AddedFlags, f)

	_, err := bencode.Marshal(pm)
	require.NoError(t, err)

	pexExtendedId := ExtensionNumber(7)
	msg := pm.Message(pexExtendedId)
	expected := []byte("\x00\x00\x00\x4c\x14\x07d5:added6:\x7f\x00\x00\x01\x55\xaa7:added.f1:\x116:added60:8:added6.f0:7:dropped0:8:dropped60:e")
	b, err := msg.MarshalBinary()
	require.NoError(t, err)
	require.EqualValues(t, b, expected)

	msg = Message{}
	dec := Decoder{
		R:         bufio.NewReader(bytes.NewReader(b)),
		MaxLength: 128,
	}
	pmOut := PexMsg{}
	err = dec.Decode(&msg)
	require.NoError(t, err)
	require.EqualValues(t, Extended, msg.Type)
	require.EqualValues(t, pexExtendedId, msg.ExtendedID)
	err = bencode.Unmarshal(msg.ExtendedPayload, &pmOut)
	require.NoError(t, err)
	require.EqualValues(t, len(pm.Added), len(pmOut.Added))
	require.EqualValues(t, pm.Added[0].IP, pmOut.Added[0].IP)
	require.EqualValues(t, pm.Added[0].Port, pmOut.Added[0].Port)
}
