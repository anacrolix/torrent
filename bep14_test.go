package torrent

import (
	"bufio"
	"bytes"
	"log"
	"net/http"
	"os"
	"testing"

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
	if ihs == nil {
		t.Error("receiver", "No Infohash")
		return
	}
	for _, ih := range ihs {
		t.Log(ih)
	}
}

func TestDiscovery(t *testing.T) {
	config := TestingConfig(t)
	config.LocalServiceDiscovery = LocalServiceDiscoveryConfig{Enabled: true, Ip6: false}

	client1, err := NewClient(config)
	require.NoError(t, err)
	defer client1.Close()
	testutil.ExportStatusWriter(client1, "1", t)

	client2, err := NewClient(config)
	require.NoError(t, err)
	defer client2.Close()
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

func waitForPeers(t *Torrent, num int) {
	t.cl.lock()
	defer t.cl.unlock()
	for {
		if t.numTotalPeers() == num {
			return
		}
		t.cl.event.Wait()
	}
}
