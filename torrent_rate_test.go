package torrent

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// rateTestSize is each test torrent's size: big enough that a limit of
// rateTestLimit takes seconds, small enough that no limit takes a moment.
const (
	rateTestSize  = 512 << 10
	rateTestLimit = 128 << 10
)

// rateTestTorrent writes a file of random bytes into dir and returns its
// torrent.
func rateTestTorrent(t *testing.T, dir, name string) *metainfo.MetaInfo {
	t.Helper()
	data := make([]byte, rateTestSize)
	_, _ = rand.Read(data)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 32 << 10}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{}
	var err error
	if mi.InfoBytes, err = bencode.Marshal(info); err != nil {
		t.Fatal(err)
	}
	return mi
}

// rateTestDownload has a leecher download two torrents from a seeder at
// once, the first limited by limit (applied by the seeder's torrent when
// up, else the leecher's), the second not, and returns how long each took.
func rateTestDownload(t *testing.T, up bool, limit *rate.Limiter) (limited, free time.Duration) {
	t.Helper()
	seedDir := t.TempDir()
	mis := []*metainfo.MetaInfo{rateTestTorrent(t, seedDir, "limited"), rateTestTorrent(t, seedDir, "free")}

	cfg := TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = seedDir
	// TestingConfig allows 5-byte requests, for tests with tiny chunks;
	// these use the usual 16 KiB ones.
	cfg.MaxAllocPeerRequestDataPerConn = NewDefaultClientConfig().MaxAllocPeerRequestDataPerConn
	seeder, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()
	cfg = TestingConfig(t)
	cfg.DataDir = t.TempDir()
	leecher, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer leecher.Close()

	var leeching []*Torrent
	for i, mi := range mis {
		st, err := seeder.AddTorrent(mi)
		if err != nil {
			t.Fatal(err)
		}
		<-st.Complete().On()
		lt, err := leecher.AddTorrent(mi)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if up {
				st.SetRateLimiters(nil, limit)
			} else {
				lt.SetRateLimiters(limit, nil)
			}
		}
		leeching = append(leeching, lt)
	}
	start := time.Now()
	took := make([]time.Duration, len(leeching))
	done := make(chan int, len(leeching))
	for i, lt := range leeching {
		lt.AddClientPeer(seeder)
		lt.DownloadAll()
		go func() {
			<-lt.Complete().On()
			took[i] = time.Since(start)
			done <- i
		}()
	}
	timeout := time.After(time.Minute)
	for range leeching {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("the downloads didn't finish in a minute")
		}
	}
	return took[0], took[1]
}

// A torrent's download limiter holds it to its limit; another torrent of
// the same client, with none, isn't slowed.
func TestTorrentDownloadRateLimiter(t *testing.T) {
	limited, free := rateTestDownload(t, false, rate.NewLimiter(rateTestLimit, 64<<10))
	checkRateTest(t, limited, free)
}

// A torrent's upload limiter holds what it sends to its limit; the
// client's other torrent, with none, isn't slowed.
func TestTorrentUploadRateLimiter(t *testing.T) {
	limited, free := rateTestDownload(t, true, rate.NewLimiter(rateTestLimit, 16<<10))
	checkRateTest(t, limited, free)
}

func checkRateTest(t *testing.T, limited, free time.Duration) {
	t.Helper()
	// Everything after the first burst comes at the limit: at least
	// (512 KiB - 64 KiB) / 128 KiB/s = 3.5 s, with a margin for timing.
	if limited < 3*time.Second {
		t.Errorf("the limited torrent took %v, want 3 s or more at %d KiB/s", limited, rateTestLimit>>10)
	}
	if free > 2*time.Second {
		t.Errorf("the torrent without a limit took %v, want well under the limited one's %v", free, limited)
	}
	t.Logf("limited %v, without a limit %v", limited, free)
}
