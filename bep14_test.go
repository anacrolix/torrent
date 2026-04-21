package torrent

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

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

func TestDiscovery(t *testing.T) {
	config := TestingConfig(t)
	config.LocalServiceDiscovery = &LocalServiceDiscoveryConfig{Ip6: false}

	client1, err := NewClient(config)
	require.NoError(t, err)
	defer t.Cleanup(func() { client1.Close() })
	testutil.ExportStatusWriter(client1, "1", t)

	client2, err := NewClient(config)
	require.NoError(t, err)
	defer t.Cleanup(func() { client2.Close() })
	testutil.ExportStatusWriter(client2, "2", t)

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	seederTorrent, _, _ := client1.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	leecherGreeting, _, _ := client2.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())

	numPeers := 1
	waitForPeers(seederTorrent, numPeers)
	require.Equal(t, numPeers, seederTorrent.numTotalPeers())
	require.Equal(t, numPeers, len(client1.lpd.peers))
	waitForPeers(leecherGreeting, numPeers)
	require.Equal(t, numPeers, leecherGreeting.numTotalPeers())
	require.Equal(t, numPeers, len(client2.lpd.peers))
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
// This covers the "LPD is the only source of local IPs" broadcast in receiver().
func TestPeerAddedToAllTorrents(t *testing.T) {
	config := TestingConfig(t)
	config.LocalServiceDiscovery = &LocalServiceDiscoveryConfig{Ip6: false}

	client1, err := NewClient(config)
	require.NoError(t, err)
	defer t.Cleanup(func() { client1.Close() })

	client2, err := NewClient(config)
	require.NoError(t, err)
	defer t.Cleanup(func() { client2.Close() })

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	// client1 has two torrents; client2 only announces the greeting torrent.
	greetingTorrent, _, _ := client1.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	otherSpec := &TorrentSpec{}
	copy(otherSpec.InfoHash[:], "other-torrent-lpd-test-00000")
	otherTorrent, _, _ := client1.AddTorrentSpec(otherSpec)

	_, _, _ = client2.AddTorrentSpec(func() *TorrentSpec {
		ret := TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return ret
	}())

	// Both should receive client2: greetingTorrent via infohash match,
	// otherTorrent via the broadcast-to-all path.
	waitForPeers(greetingTorrent, 1)
	waitForPeers(otherTorrent, 1)

	require.Equal(t, 1, greetingTorrent.numTotalPeers())
	require.Equal(t, 1, otherTorrent.numTotalPeers())
}

// TestNewTorrentGetsExistingPeers verifies that when a torrent is added after
// LPD has already discovered peers, those peers are immediately injected via
// the lpdPeers() call inside AddTorrentSpec.
func TestNewTorrentGetsExistingPeers(t *testing.T) {
	config := TestingConfig(t)
	config.LocalServiceDiscovery = &LocalServiceDiscoveryConfig{Ip6: false}

	client1, err := NewClient(config)
	require.NoError(t, err)
	defer t.Cleanup(func() { client1.Close() })

	client2, err := NewClient(config)
	require.NoError(t, err)
	defer t.Cleanup(func() { client2.Close() })

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	greetingTorrent, _, _ := client1.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	_, _, _ = client2.AddTorrentSpec(func() *TorrentSpec {
		ret := TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return ret
	}())

	// Wait until LPD discovery has happened and client2 is in client1's peer cache.
	waitForPeers(greetingTorrent, 1)
	require.Len(t, client1.lpd.peers, 1)

	// Add a new torrent after discovery. lpdPeers() is called synchronously
	// inside AddTorrentSpec, so client2 should already be a peer on return.
	newSpec := &TorrentSpec{}
	copy(newSpec.InfoHash[:], "new-torrent-after-discovery00")
	newTorrent, _, _ := client1.AddTorrentSpec(newSpec)

	waitForPeers(newTorrent, 1)
	require.Equal(t, 1, newTorrent.numTotalPeers())
}

// TestReceiverMalformedMessages verifies that the receiver silently drops
// messages with a wrong method, a missing Infohash header, or a missing
// Port header, without adding any peers.
func TestReceiverMalformedMessages(t *testing.T) {
	config := TestingConfig(t)
	config.LocalServiceDiscovery = &LocalServiceDiscoveryConfig{Ip6: false}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer cl.Close()

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	tor, _, _ := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))

	addr, err := net.ResolveUDPAddr("udp4", bep14Host4)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp4", nil, addr)
	require.NoError(t, err)
	defer conn.Close()

	msgs := []string{
		// Wrong HTTP method — should be BT-SEARCH.
		"GET * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nPort: 9999\r\nInfohash: AABBCCDD1122334455667788AABBCCDD11223344\r\n\r\n\r\n",
		// Missing Infohash header.
		"BT-SEARCH * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nPort: 9999\r\n\r\n\r\n",
		// Missing Port header.
		"BT-SEARCH * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nInfohash: AABBCCDD1122334455667788AABBCCDD11223344\r\n\r\n\r\n",
	}
	for _, msg := range msgs {
		_, err = conn.Write([]byte(msg))
		require.NoError(t, err)
	}

	time.Sleep(200 * time.Millisecond)

	require.Equal(t, 0, tor.numTotalPeers(), "malformed messages should not add peers")
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

func waitForPeers(t *Torrent, num int) {
	t.cl.lock()
	defer t.cl.unlock()
	for {
		log.Println("waitForPeers", "numTotalPeers", t.numTotalPeers(), "num", num)
		if t.numTotalPeers() == num {
			return
		}
		t.cl.event.Wait()
	}
}
