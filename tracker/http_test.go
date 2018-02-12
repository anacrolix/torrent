package tracker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/bencode"
)

var defaultHTTPUserAgent = "Go-Torrent"

func TestUnmarshalHTTPResponsePeerDicts(t *testing.T) {
	var hr httpResponse
	require.NoError(t, bencode.Unmarshal(
		[]byte("d5:peersl"+
			"d2:ip7:1.2.3.47:peer id20:thisisthe20bytepeeri4:porti9999ee"+
			"d7:peer id20:thisisthe20bytepeeri2:ip39:2001:0db8:85a3:0000:0000:8a2e:0370:73344:porti9998ee"+
			"e"+
			"6:peers618:123412341234123456"+
			"e"),
		&hr))

	require.Len(t, hr.Peers, 2)
	assert.Equal(t, []byte("thisisthe20bytepeeri"), hr.Peers[0].ID)
	assert.EqualValues(t, 9999, hr.Peers[0].Port)
	assert.EqualValues(t, 9998, hr.Peers[1].Port)
	assert.NotNil(t, hr.Peers[0].IP)
	assert.NotNil(t, hr.Peers[1].IP)

	assert.Len(t, hr.Peers6, 1)
	assert.EqualValues(t, "1234123412341234", hr.Peers6[0].IP)
	assert.EqualValues(t, 0x3536, hr.Peers6[0].Port)
}

func TestUnmarshalHttpResponseNoPeers(t *testing.T) {
	var hr httpResponse
	require.NoError(t, bencode.Unmarshal(
		[]byte("d6:peers618:123412341234123456e"),
		&hr,
	))
	require.Len(t, hr.Peers, 0)
	assert.Len(t, hr.Peers6, 1)
}

func TestUnmarshalHttpResponsePeers6NotCompact(t *testing.T) {
	var hr httpResponse
	require.Error(t, bencode.Unmarshal(
		[]byte("d6:peers6lee"),
		&hr,
	))
}
