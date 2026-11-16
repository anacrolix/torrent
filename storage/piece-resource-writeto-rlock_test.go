package storage

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/filecache"
	"github.com/anacrolix/missinggo/v2/resource"

	"github.com/anacrolix/torrent/metainfo"
)

// hangProvider does not implement ConsecutiveChunkReader. Incomplete WriteTo
// therefore falls back to io.SectionReader(piece), which calls ReadAt → NewReader
// and takes the piece RLock a second time. Blocking completed Stat once lets a
// concurrent MarkComplete queue for Lock between those two RLocks.
type hangProvider struct {
	resource.Provider
	statOnce sync.Once
	entered  chan struct{}
	release  chan struct{}
}

func (p *hangProvider) NewInstance(name string) (resource.Instance, error) {
	i, err := p.Provider.NewInstance(name)
	if err != nil {
		return nil, err
	}
	return hangInstance{Instance: i, p: p, name: name}, nil
}

type hangInstance struct {
	resource.Instance
	p    *hangProvider
	name string
}

func (i hangInstance) Stat() (fs.FileInfo, error) {
	if strings.HasPrefix(i.name, "completed/") {
		i.p.statOnce.Do(func() {
			close(i.p.entered)
			<-i.p.release
		})
	}
	return i.Instance.Stat()
}

func (i hangInstance) Readdirnames() ([]string, error) {
	return i.Instance.(resource.DirInstance).Readdirnames()
}

func TestWriteToFallbackRecursiveRLockDeadlock(t *testing.T) {
	cache, err := filecache.NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prov := &hangProvider{
		Provider: cache.AsResourceProvider(),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}

	info := metainfo.Info{
		Files:       []metainfo.FileInfo{{Path: []string{"p"}, Length: 4}},
		PieceLength: 4,
		Pieces:      make([]byte, 20),
	}
	ti, err := NewResourcePieces(prov).OpenTorrent(
		context.Background(), &info, metainfo.HashBytes([]byte("t")))
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()

	piece := ti.PieceWithHash(info.Piece(0), g.Some(make([]byte, 20)))
	if _, err := piece.WriteAt([]byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	wt, ok := piece.(io.WriterTo)
	if !ok {
		t.Fatal("piecePerResourcePiece should implement io.WriterTo")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := wt.WriteTo(io.Discard)
		writeDone <- err
	}()

	select {
	case <-prov.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("WriteTo never reached completed Stat under outer RLock")
	}

	markDone := make(chan error, 1)
	go func() { markDone <- piece.MarkComplete() }()

	// Let MarkComplete block on Lock behind WriteTo's held RLock.
	time.Sleep(50 * time.Millisecond)
	close(prov.release)

	timeout := time.After(3 * time.Second)
	var gotWrite, gotMark bool
	for !gotWrite || !gotMark {
		select {
		case err := <-writeDone:
			gotWrite = true
			if err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
		case err := <-markDone:
			gotMark = true
			if err != nil {
				t.Fatalf("MarkComplete: %v", err)
			}
		case <-timeout:
			t.Fatalf("deadlock: WriteTo fallback re-acquires piece RLock via ReadAt/NewReader while MarkComplete waits for write lock (writeDone=%v markDone=%v)", gotWrite, gotMark)
		}
	}
}
