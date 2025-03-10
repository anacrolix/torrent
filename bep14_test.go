package torrent

import (
	"bufio"
	"bytes"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/stretchr/testify/require"
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
	var ihs []string = req.Header[http.CanonicalHeaderKey("Infohash")]
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
	config.LocalServiceDiscovery = LocalServiceDiscoveryConfig{Enabled: true}

	client1, err := NewClient(config)
	require.NoError(t, err)
	defer client1.Close()
	
	client2, err := NewClient(config)
	require.NoError(t, err)
	defer client2.Close()

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	seederTorrent, _, _ := client1.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	leecherGreeting, _, _ := client2.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())

	time.Sleep(5 * time.Second)
	waitForPeers(seederTorrent)
	require.Equal(t, seederTorrent.numTotalPeers(), 2)
	require.Equal(t, len(client1.lpd.peers), 2)
	waitForPeers(leecherGreeting)
	require.Equal(t, leecherGreeting.numTotalPeers(), 2)
	require.Equal(t, len(client2.lpd.peers), 2)
}

func waitForPeers(t *Torrent) {
	t.cl.lock()
	defer t.cl.unlock()
	for {
		if t.numTotalPeers() > 0 {
			return
		}
		t.cl.event.Wait()
	}
}
