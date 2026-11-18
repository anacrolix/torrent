package torrent

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// UDP trackers share a socket, so the number of file descriptors doesn't grow with the number of
// tracker URLs.
func TestUdpTrackersShareSocket(t *testing.T) {
	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	qt.Assert(t, qt.IsNil(err))
	defer srv.Close()

	var mu sync.Mutex
	var listens int
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.TrackerListenPacket = func(network, addr string) (net.PacketConn, error) {
		mu.Lock()
		listens++
		mu.Unlock()
		return net.ListenPacket(network, addr)
	}
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	const numTrackers = 50
	var trackers []string
	for i := range numTrackers {
		trackers = append(trackers, fmt.Sprintf("udp://%s/announce/%d", srv.LocalAddr(), i))
	}
	_, _, err = cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash: metainfo.HashBytes([]byte("udp tracker socket sharing")),
		},
		Trackers: [][]string{trackers},
	})
	qt.Assert(t, qt.IsNil(err))

	// Each tracker sends its own connect request. Wait for all of them.
	srv.SetReadDeadline(time.Now().Add(20 * time.Second))
	b := make([]byte, 0x800)
	for range numTrackers {
		_, _, err := srv.ReadFrom(b)
		qt.Assert(t, qt.IsNil(err))
	}
	mu.Lock()
	defer mu.Unlock()
	// One for udp4. The udp6 trackers can't resolve an IPv4 address, so never open their socket.
	qt.Assert(t, qt.Equals(listens, 1))
}
