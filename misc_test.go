package torrent

import (
	"reflect"
	"strings"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/davecgh/go-spew/spew"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

func TestTorrentOffsetRequest(t *testing.T) {
	check := func(tl, ps, off int64, expected Request, ok bool) {
		req, _ok := torrentOffsetRequest(tl, ps, defaultChunkSize, off)
		qt.Check(t, qt.Equals(ok, _ok))
		qt.Check(t, qt.Equals(expected, req))
	}
	check(13, 5, 0, newRequest(0, 0, 5), true)
	check(13, 5, 3, newRequest(0, 0, 5), true)
	check(13, 5, 11, newRequest(2, 0, 3), true)
	check(13, 5, 13, Request{}, false)
}

func TestSpewConnStats(t *testing.T) {
	s := spew.Sdump(ConnStats{})
	t.Logf("\n%s", s)
	lines := strings.Count(s, "\n")
	qt.Check(t, qt.Equals(lines, 2+reflect.ValueOf(ConnStats{}).NumField()))
}

func TestValidateInfoShortV2Root(t *testing.T) {
	info := metainfo.Info{
		MetaVersion: 2,
		PieceLength: 16384,
		Name:        "x",
		FileTree: metainfo.FileTree{
			Dir: map[string]metainfo.FileTree{
				"a": {File: metainfo.FileTreeFile{Length: 1, PiecesRoot: "short"}},
			},
		},
	}
	err := validateInfo(&info)
	if err == nil {
		t.Fatal("expected validateInfo to reject short pieces root")
	}
	if !strings.Contains(err.Error(), "pieces root length 5") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateInfoMissingV2Root(t *testing.T) {
	info := metainfo.Info{
		MetaVersion: 2,
		PieceLength: 16384,
		Name:        "x",
		FileTree: metainfo.FileTree{
			Dir: map[string]metainfo.FileTree{
				"a": {File: metainfo.FileTreeFile{Length: 100, PiecesRoot: ""}},
			},
		},
	}
	err := validateInfo(&info)
	if err == nil {
		t.Fatal("expected validateInfo to reject missing pieces root on non-empty file")
	}
	if !strings.Contains(err.Error(), "pieces root length 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateInfoValidV2(t *testing.T) {
	valid32 := strings.Repeat("x", 32)
	info := metainfo.Info{
		MetaVersion: 2,
		PieceLength: 16384,
		Name:        "x",
		FileTree: metainfo.FileTree{
			Dir: map[string]metainfo.FileTree{
				"a": {File: metainfo.FileTreeFile{Length: 1024, PiecesRoot: valid32}},
			},
		},
	}
	err := validateInfo(&info)
	if err != nil {
		t.Fatalf("expected valid v2 info to pass, got: %v", err)
	}
}

// End-to-end: AddTorrentSpec with a crafted v2 info dict containing a short pieces root
// must return an error instead of panicking through PiecesRootAsByteArray.
func TestAddTorrentSpecBadV2Root(t *testing.T) {
	infoBytes := append(
		[]byte("d9:file treed1:ad0:d6:lengthi1e11:pieces root5:shorteee6:lengthi1e12:meta versioni2e4:name1:x12:piece lengthi16384e6:pieces20:"),
		make([]byte, metainfo.HashSize)...)
	infoBytes = append(infoBytes, 'e')

	cl, err := NewClient(TestingConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	_, _, err = cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:   metainfo.HashBytes(infoBytes),
			InfoHashV2: g.Some(infohash_v2.HashBytes(infoBytes)),
			InfoBytes:  infoBytes,
		},
	})
	if err == nil {
		t.Fatal("expected AddTorrentSpec to reject malformed v2 pieces root")
	}
	if !strings.Contains(err.Error(), "bad v2 file tree") {
		t.Fatalf("unexpected error: %v", err)
	}
}
