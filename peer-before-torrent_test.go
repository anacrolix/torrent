package torrent

import (
	"net"
	"testing"

	"github.com/go-quicktest/qt"
)

// The NewPeer callback gets a Peer before the handshake has said which Torrent it's for. Its
// exported methods took the Client lock through the Torrent, so they panicked on a nil pointer.
func TestPeerMethodsBeforeTorrentIsKnown(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	ours, theirs := net.Pipe()
	defer theirs.Close()
	c := cl.newConnection(ours, newConnectionOpts{
		remoteAddr: ours.RemoteAddr(),
		network:    "pipe",
	})
	qt.Check(t, qt.Equals(c.Stats().DownloadRate, 0.))
	qt.Check(t, qt.Equals(c.DownloadRate(), 0.))
	qt.Check(t, qt.IsNil(c.Close()))
	qt.Check(t, qt.IsTrue(c.closed.IsSet()))
}
