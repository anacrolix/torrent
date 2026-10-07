package torrent

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// Torrents dropped after getting their info from a peer should be garbage collected.
func TestIssue930DroppedTorrentsCollected(t *testing.T) {
	seederCfg := TestingConfig(t)
	seederCfg.Seed = true
	seeder, err := NewClient(seederCfg)
	qt.Assert(t, qt.IsNil(err))
	defer seeder.Close()
	leecher := newTestingClient(t)
	var dropped []weak.Pointer[Torrent]
	for i := range 10 {
		path := filepath.Join(seederCfg.DataDir, fmt.Sprintf("t%d", i))
		data := make([]byte, 2000*1024)
		rand.Read(data)
		qt.Assert(t, qt.IsNil(os.WriteFile(path, data, 0o644)))
		info := metainfo.Info{PieceLength: 1024}
		qt.Assert(t, qt.IsNil(info.BuildFromFilePath(path)))
		var mi metainfo.MetaInfo
		mi.InfoBytes, err = bencode.Marshal(info)
		qt.Assert(t, qt.IsNil(err))
		st, err := seeder.AddTorrent(&mi)
		qt.Assert(t, qt.IsNil(err))
		<-st.Complete().On()
		lt, _ := leecher.AddTorrentInfoHash(mi.HashInfoBytes())
		lt.AddClientPeer(seeder)
		<-lt.GotInfo()
		dropped = append(dropped, weak.Make(lt))
		lt.Drop()
		st.Drop()
	}
	// Peer conns close asynchronously after Drop.
	deadline := time.Now().Add(15 * time.Second)
	for i, wp := range dropped {
		for {
			runtime.GC()
			if wp.Value() == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		qt.Check(t, qt.IsNil(wp.Value()), qt.Commentf("torrent %d", i))
	}
}
