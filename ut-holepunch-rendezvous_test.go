package torrent

import (
	"net/netip"
	"testing"

	qt "github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
	utHolepunch "github.com/anacrolix/torrent/peer_protocol/ut-holepunch"
)

// A peer that never advertised ut_holepunch sends a rendezvous addressed with our advertised id, on
// a torrent with no info. Replying used to panic in wrapUtHolepunchMsgForPeerConn.
func TestHolepunchRendezvousFromSenderWithoutExtension(t *testing.T) {
	_, c := newNetworkBoundsTestConn(t, TestingConfig(t), false)
	c.RemoteAddr = netip.MustParseAddrPort("203.0.113.5:6881")
	c.PeerExtensionIDs = map[pp.ExtensionName]pp.ExtensionNumber{}
	c.LocalLtepProtocolMap = &c.t.cl.defaultLocalLtepProtocolMap
	ourID := c.LocalLtepProtocolMap.toSupportedExtensionDict()[utHolepunch.ExtensionName]
	qt.Assert(t, qt.Not(qt.Equals(ourID, 0)))
	msg := utHolepunch.Msg{
		MsgType:  utHolepunch.Rendezvous,
		AddrPort: netip.MustParseAddrPort("192.0.2.1:6881"),
	}
	payload, err := msg.MarshalBinary()
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsNil(c.onReadExtendedMsg(ourID, payload)))
}
