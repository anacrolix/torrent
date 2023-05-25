package httpTracker

import (
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/tracker/udp"
)

func TestUnmarshalHTTPResponsePeerDicts(t *testing.T) {
	var hr HttpResponse
	require.NoError(t, bencode.Unmarshal(
		[]byte("d5:peersl"+
			"d2:ip7:1.2.3.47:peer id20:thisisthe20bytepeeri4:porti9999ee"+
			"d2:ip39:2001:0db8:85a3:0000:0000:8a2e:0370:73347:peer id20:thisisthe20bytepeeri4:porti9998ee"+
			"e"+
			"6:peers618:123412341234123456"+
			"e"),
		&hr))

	require.Len(t, hr.Peers.List, 2)
	assert.Equal(t, []byte("thisisthe20bytepeeri"), hr.Peers.List[0].ID)
	assert.EqualValues(t, 9999, hr.Peers.List[0].Port)
	assert.EqualValues(t, 9998, hr.Peers.List[1].Port)
	assert.NotNil(t, hr.Peers.List[0].IP)
	assert.NotNil(t, hr.Peers.List[1].IP)

	assert.Len(t, hr.Peers6, 1)
	assert.EqualValues(t, "1234123412341234", hr.Peers6[0].IP)
	assert.EqualValues(t, 0x3536, hr.Peers6[0].Port)
}

func TestUnmarshalHttpResponseNoPeers(t *testing.T) {
	var hr HttpResponse
	require.NoError(t, bencode.Unmarshal(
		[]byte("d6:peers618:123412341234123456e"),
		&hr,
	))
	require.Len(t, hr.Peers.List, 0)
	assert.Len(t, hr.Peers6, 1)
}

func TestUnmarshalHttpResponsePeers6NotCompact(t *testing.T) {
	var hr HttpResponse
	require.Error(t, bencode.Unmarshal(
		[]byte("d6:peers6lee"),
		&hr,
	))
}

// Checks that infohash bytes that correspond to spaces are escaped with %20 instead of +. See
// https://github.com/anacrolix/torrent/issues/534
func TestSetAnnounceInfohashParamWithSpaces(t *testing.T) {
	someUrl := &url.URL{}
	ihBytes := [20]uint8{
		0x2b, 0x76, 0xa, 0xa1, 0x78, 0x93, 0x20, 0x30, 0xc8, 0x47,
		0xdc, 0xdf, 0x8e, 0xae, 0xbf, 0x56, 0xa, 0x1b, 0xd1, 0x6c,
	}
	setAnnounceParams(
		someUrl,
		&udp.AnnounceRequest{
			InfoHash: ihBytes,
		},
		AnnounceOpt{})
	t.Logf("%q", someUrl)
	qt.Assert(t, someUrl.Query().Get("info_hash"), qt.Equals, string(ihBytes[:]))
	qt.Check(t,
		someUrl.String(),
		qt.Contains,
		"info_hash=%2Bv%0A%A1x%93%200%C8G%DC%DF%8E%AE%BFV%0A%1B%D1l")
}
