// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package torrent

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/log"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
)

func TestMultiInfohash(t *testing.T) {
	html := `BT-SEARCH * HTTP/1.1
Host: 239.192.152.143:6771
Port: 3333
Infohash: 123123
Infohash: 2222
cookie: name=value


`
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader([]byte(html))))
	if err != nil {
		t.Error("receiver", err)
		return
	}
	ihs := req.Header[http.CanonicalHeaderKey("Infohash")]
	require.Equal(t, []string{"123123", "2222"}, ihs)
	require.Equal(t, "3333", req.Header.Get("Port"))
}

// setupTestLPD installs a mock lpdServer on cl. The conn4 has no real
// multicast sockets — tests drive the receive path synchronously via
// injectAnnounce, so behavior doesn't depend on the host OS delivering
// multicast loopback. Replaces turning on real LPD in the client config.
func setupTestLPD(cl *Client) {
	lpd := &lpdServer{peers: make(map[string]time.Time)}
	lpd.conn4 = &lpdConn{
		force:   make(chan struct{}, 1),
		lpd:     lpd,
		network: "udp4",
		host:    bep14Host4,
		logger:  log.Default,
	}
	cl.lpd = lpd
}

// injectAnnounce crafts a BT-SEARCH announce for infohashes as if it arrived
// from peerAddr and feeds it to cl's LPD receive path. The resulting peer
// entry is (peerAddr.IP : peerAddr.Port).
func injectAnnounce(t *testing.T, cl *Client, peerAddr *net.UDPAddr, infohashes []string) {
	t.Helper()
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "BT-SEARCH * HTTP/1.1\r\nHost: %s\r\nPort: %d\r\n", bep14Host4, peerAddr.Port)
	for _, ih := range infohashes {
		fmt.Fprintf(&buf, "Infohash: %s\r\n", strings.ToUpper(ih))
	}
	buf.WriteString("\r\n\r\n")
	cl.lpd.conn4.handleAnnouncePacket(cl, buf.Bytes(), peerAddr)
}

func TestDiscovery(t *testing.T) {
	config := TestingConfig(t)

	client1, err := NewClient(config)
	require.NoError(t, err)
	t.Cleanup(func() { client1.Close() })
	setupTestLPD(client1)
	testutil.ExportStatusWriter(client1, "1", t)

	client2, err := NewClient(config)
	require.NoError(t, err)
	t.Cleanup(func() { client2.Close() })
	setupTestLPD(client2)
	testutil.ExportStatusWriter(client2, "2", t)

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	seederTorrent, _, _ := client1.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	leecherGreeting, _, _ := client2.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())

	ih := mi.HashInfoBytes().HexString()
	peer2 := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: client2.LocalPort()}
	peer1 := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: client1.LocalPort()}
	injectAnnounce(t, client1, peer2, []string{ih})
	injectAnnounce(t, client2, peer1, []string{ih})

	waitForPeers(t, seederTorrent, 1)
	require.Equal(t, 1, seederTorrent.numTotalPeers())
	require.Equal(t, 1, len(client1.lpd.peers))
	waitForPeers(t, leecherGreeting, 1)
	require.Equal(t, 1, leecherGreeting.numTotalPeers())
	require.Equal(t, 1, len(client2.lpd.peers))
}

// TestLPDPeerDeduplication verifies that adding the same peer address twice
// results in only one entry in the peers map.
func TestLPDPeerDeduplication(t *testing.T) {
	lpd := &lpdServer{peers: make(map[string]time.Time)}
	lpd.peer("1.2.3.4:6881")
	lpd.peer("1.2.3.4:6881")
	require.Len(t, lpd.peers, 1)
}

// TestLPDPeerExpiry verifies that stale peers are removed after
// 2 * bep14LongTimeout without activity, and that fresh peers survive.
func TestLPDPeerExpiry(t *testing.T) {
	lpd := &lpdServer{peers: make(map[string]time.Time)}

	// Inject a stale entry directly into the map.
	lpd.peers["1.2.3.4:6881"] = time.Now().Add(-3 * bep14LongTimeout)
	lpd.refresh()
	require.Empty(t, lpd.peers, "stale peer should be removed after refresh")

	// A fresh peer should survive a refresh.
	lpd.peer("5.6.7.8:6881")
	lpd.refresh()
	require.Len(t, lpd.peers, 1, "fresh peer should survive refresh")
}

// TestPeerAddedToAllTorrents verifies that a peer discovered via LPD is added
// to all active torrents, not only the one matching the announced infohash.
// This covers the "LPD is the only source of local IPs" broadcast in
// OnLPDAnnouncement.
func TestPeerAddedToAllTorrents(t *testing.T) {
	config := TestingConfig(t)

	cl, err := NewClient(config)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	setupTestLPD(cl)

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	greetingTorrent, _, _ := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	otherSpec := &TorrentSpec{}
	copy(otherSpec.InfoHash[:], "other-torrent-lpd-test-00000")
	otherTorrent, _, _ := cl.AddTorrentSpec(otherSpec)

	// Announce only greetingTorrent's infohash. The "broadcast to all"
	// behavior should still cause otherTorrent to receive the peer.
	peer := &net.UDPAddr{IP: net.IPv4(10, 20, 30, 40), Port: 7777}
	injectAnnounce(t, cl, peer, []string{greetingTorrent.InfoHash().HexString()})

	waitForPeers(t, greetingTorrent, 1)
	waitForPeers(t, otherTorrent, 1)
	require.Equal(t, 1, greetingTorrent.numTotalPeers())
	require.Equal(t, 1, otherTorrent.numTotalPeers())
}

// TestNewTorrentGetsExistingPeers verifies that when a torrent is added after
// LPD has already discovered peers, those peers are immediately injected via
// the lpdPeers() call inside AddTorrentSpec.
func TestNewTorrentGetsExistingPeers(t *testing.T) {
	config := TestingConfig(t)

	cl, err := NewClient(config)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	setupTestLPD(cl)

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	greetingTorrent, _, _ := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))

	// Record an LPD peer before adding the second torrent.
	peer := &net.UDPAddr{IP: net.IPv4(10, 11, 12, 13), Port: 6881}
	injectAnnounce(t, cl, peer, []string{greetingTorrent.InfoHash().HexString()})
	waitForPeers(t, greetingTorrent, 1)
	require.Len(t, cl.lpd.peers, 1)

	// A torrent added now should synchronously receive the known LPD peer
	// through AddTorrentSpec's cl.lpd.lpdPeers(t) call.
	newSpec := &TorrentSpec{}
	copy(newSpec.InfoHash[:], "new-torrent-after-discovery00")
	newTorrent, _, _ := cl.AddTorrentSpec(newSpec)

	waitForPeers(t, newTorrent, 1)
	require.Equal(t, 1, newTorrent.numTotalPeers())
}

// TestReceiverMalformedMessages verifies that the receiver silently drops
// messages with a wrong method, a missing Infohash header, or a missing
// Port header, without adding any peers.
func TestReceiverMalformedMessages(t *testing.T) {
	config := TestingConfig(t)

	cl, err := NewClient(config)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	setupTestLPD(cl)

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	tor, _, _ := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))

	from := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 6881}
	msgs := []string{
		// Wrong HTTP method — should be BT-SEARCH.
		"GET * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nPort: 9999\r\nInfohash: AABBCCDD1122334455667788AABBCCDD11223344\r\n\r\n\r\n",
		// Missing Infohash header.
		"BT-SEARCH * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nPort: 9999\r\n\r\n\r\n",
		// Missing Port header.
		"BT-SEARCH * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nInfohash: AABBCCDD1122334455667788AABBCCDD11223344\r\n\r\n\r\n",
	}
	for _, msg := range msgs {
		cl.lpd.conn4.handleAnnouncePacket(cl, []byte(msg), from)
	}

	require.Equal(t, 0, tor.numTotalPeers(), "malformed messages should not add peers")
	require.Empty(t, cl.lpd.peers, "malformed messages should not record LPD peers")
}

// TestBuildAnnouncePacketSingle verifies that a small queue produces one packet
// containing every infohash and reports a completed rotation.
func TestBuildAnnouncePacketSingle(t *testing.T) {
	queue := []string{
		"aabbccddeeff00112233445566778899aabbccdd",
		"1122334455667788990011223344556677889900",
	}
	packet, nextIdx, rotated := buildAnnouncePacket(bep14Host4, 6881, queue, 0, bep14MaxPacketSize)
	require.NotEmpty(t, packet)
	require.Zero(t, nextIdx)
	require.True(t, rotated)
	body := string(packet)
	for _, ih := range queue {
		require.Contains(t, body, strings.ToUpper(ih))
	}
}

// TestBuildAnnouncePacketFragments verifies that a queue too large for a
// single packet is split across successive calls: each packet stays under the
// size cap, every infohash is covered exactly once across the series, and
// rotation is only reported when the queue has been fully drained.
func TestBuildAnnouncePacketFragments(t *testing.T) {
	const n = 40 // 40 x ~51B infohash lines overflow the 1400B cap
	queue := make([]string, n)
	for i := range queue {
		queue[i] = fmt.Sprintf("%040x", i)
	}

	seen := make(map[string]int, n)
	packets := 0
	idx := 0
	for {
		packet, nextIdx, rotated := buildAnnouncePacket(bep14Host4, 6881, queue, idx, bep14MaxPacketSize)
		require.NotEmpty(t, packet)
		require.Less(t, len(packet), bep14MaxPacketSize)
		packets++
		end := nextIdx
		if rotated {
			end = n
		}
		for i := idx; i < end; i++ {
			seen[strings.ToUpper(queue[i])]++
		}
		if rotated {
			require.Zero(t, nextIdx)
			break
		}
		require.Greater(t, nextIdx, idx, "must make progress")
		idx = nextIdx
	}

	require.Greater(t, packets, 1, "queue should require more than one packet")
	require.Len(t, seen, n, "every infohash must appear")
	for ih, count := range seen {
		require.Equal(t, 1, count, "%s announced %d times, want 1", ih, count)
	}
}

// TestBuildAnnouncePacketEmptyQueue verifies that an empty queue yields no
// packet and is treated as a (trivially) completed rotation.
func TestBuildAnnouncePacketEmptyQueue(t *testing.T) {
	packet, nextIdx, rotated := buildAnnouncePacket(bep14Host4, 6881, nil, 0, bep14MaxPacketSize)
	require.Nil(t, packet)
	require.Zero(t, nextIdx)
	require.True(t, rotated)
}

// waitForPeers blocks until tor has exactly num peers, or fails the test if
// that doesn't happen within the deadline. Without this bound, a missing
// wakeup would hang the entire `go test` run until its 10-minute kill switch.
func waitForPeers(t *testing.T, tor *Torrent, num int) {
	t.Helper()
	const deadline = 30 * time.Second
	// A timer-driven Broadcast wakes the Wait below so the loop can re-check
	// the deadline even if no peer-state change broadcasts happen.
	var wakeOnce sync.Once
	timer := time.AfterFunc(deadline, func() {
		wakeOnce.Do(func() {
			tor.cl.lock()
			tor.cl.event.Broadcast()
			tor.cl.unlock()
		})
	})
	defer timer.Stop()

	tor.cl.lock()
	defer tor.cl.unlock()
	start := time.Now()
	for tor.numTotalPeers() != num {
		if time.Since(start) >= deadline {
			t.Fatalf("timed out after %s waiting for %d peers, have %d", deadline, num, tor.numTotalPeers())
		}
		tor.cl.event.Wait()
	}
}
