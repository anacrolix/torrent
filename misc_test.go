package torrent

import (
	"math"
	"reflect"
	"strings"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/davecgh/go-spew/spew"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
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

// Malformed info dicts that would otherwise divide by zero, index out of range or allocate without
// bound in setInfo must be rejected by validateInfo first.
func TestValidateInfoRejectsMalformed(t *testing.T) {
	valid32 := strings.Repeat("x", 32)
	for _, c := range []struct {
		name string
		info metainfo.Info
		want string
	}{
		{
			"v1 negative piece length",
			metainfo.Info{Name: "x", PieceLength: -1, Length: 1, Pieces: make([]byte, 20)},
			"negative piece length",
		},
		{
			"piece length too large",
			metainfo.Info{Name: "x", PieceLength: 1 << 32, Length: 1, Pieces: make([]byte, 20)},
			"piece length",
		},
		{
			"v1 negative file length",
			metainfo.Info{Name: "x", PieceLength: 16384, Pieces: make([]byte, 20), Files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: -1},
				{Path: []string{"b"}, Length: 17},
			}},
			"negative file length",
		},
		{
			"v1 negative single file length",
			metainfo.Info{Name: "x", PieceLength: 16384, Length: -1},
			"negative file length",
		},
		{
			"v1 file lengths overflow",
			metainfo.Info{Name: "x", PieceLength: 16384, Pieces: make([]byte, 20), Files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: math.MaxInt64},
				{Path: []string{"b"}, Length: math.MaxInt64},
				{Path: []string{"c"}, Length: 3},
			}},
			"overflows",
		},
		{
			"v2 zero piece length",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 0, FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"a": {File: metainfo.FileTreeFile{Length: 0}}},
			}},
			"not a power of two",
		},
		{
			"v2 non-power-of-two piece length",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 3 * 16384, FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"a": {File: metainfo.FileTreeFile{Length: 16384, PiecesRoot: valid32}}},
			}},
			"not a power of two",
		},
		{
			"v2 negative file length",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"a": {File: metainfo.FileTreeFile{Length: -1, PiecesRoot: valid32}}},
			}},
			"negative length",
		},
		{
			"v2 directory node with file properties",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"d": {
					File: metainfo.FileTreeFile{Length: 16384},
					Dir:  map[string]metainfo.FileTree{"a": {File: metainfo.FileTreeFile{Length: 16384, PiecesRoot: valid32}}},
				}},
			}},
			"file properties",
		},
		{
			"v2 file tree implies too many pieces",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"a": {File: metainfo.FileTreeFile{Length: 1 << 50, PiecesRoot: valid32}}},
			}},
			"more than",
		},
		{
			"hybrid with too few v1 piece hashes",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Length: 5 * 16384, Pieces: make([]byte, 20), FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"x": {File: metainfo.FileTreeFile{Length: 5 * 16384, PiecesRoot: valid32}}},
			}},
			"v1 piece hashes",
		},
		{
			// v1 files cover fewer pieces than the v2 tree. The last piece's v1 length comes out
			// negative and panics the piece hasher if this isn't rejected.
			"hybrid v1 file lengths shorter than v2 file tree",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Length: 16384, Pieces: make([]byte, 40), FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"x": {File: metainfo.FileTreeFile{Length: 2 * 16384, PiecesRoot: valid32}}},
			}},
			"v1 file lengths",
		},
		{
			// No v1 length at all with the v2 tree spanning two pieces: the last piece's v1 length is
			// negative.
			"hybrid v1 length missing",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Pieces: make([]byte, 40), FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"x": {File: metainfo.FileTreeFile{Length: 2 * 16384, PiecesRoot: valid32}}},
			}},
			"v1 file lengths",
		},
		{
			// v1 files cover more pieces than the v2 tree. The last piece's v1 length then exceeds the
			// piece length, which also panics.
			"hybrid v1 file lengths longer than v2 file tree",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Length: 3 * 16384, Pieces: make([]byte, 40), FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"x": {File: metainfo.FileTreeFile{Length: 2 * 16384, PiecesRoot: valid32}}},
			}},
			"v1 file lengths",
		},
		{
			// A zero piece length with piece hashes is contradictory: the piece count comes from the
			// hashes, but the last piece's v1 length is zero and panics the piece hasher.
			"v1 zero piece length with piece hashes",
			metainfo.Info{Name: "x", PieceLength: 0, Pieces: make([]byte, 40)},
			"zero piece length",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := validateInfo(&c.info)
			if err == nil {
				t.Fatal("expected validateInfo to reject malformed info")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

// The stricter validation must still accept well-formed v1 and hybrid infos (valid v2 is covered by
// TestValidateInfoValidV2).
func TestValidateInfoAcceptsWellFormed(t *testing.T) {
	valid32 := strings.Repeat("x", 32)
	for _, c := range []struct {
		name string
		info metainfo.Info
	}{
		{
			"v1",
			metainfo.Info{Name: "x", PieceLength: 16384, Length: 16385, Pieces: make([]byte, 40)},
		},
		{
			// An empty torrent has no content and no pieces, so a zero piece length is fine here.
			"v1 empty torrent with zero piece length",
			metainfo.Info{Name: "x", PieceLength: 0},
		},
		{
			"hybrid single piece",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Length: 16384, Pieces: make([]byte, 20), FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"x": {File: metainfo.FileTreeFile{Length: 16384, PiecesRoot: valid32}}},
			}},
		},
		{
			"hybrid multiple pieces",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Length: 3 * 16384, Pieces: make([]byte, 60), FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{"x": {File: metainfo.FileTreeFile{Length: 3 * 16384, PiecesRoot: valid32}}},
			}},
		},
		{
			// A padded hybrid: file "a" plus a pad file fills the first piece, then file "b" fills
			// the second. Two v2 pieces, and the v1 lengths (including the pad) span two pieces too.
			"hybrid multiple files with padding",
			metainfo.Info{MetaVersion: 2, Name: "x", PieceLength: 16384, Pieces: make([]byte, 40), Files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 100},
				{Path: []string{".pad", "16284"}, Length: 16284},
				{Path: []string{"b"}, Length: 16384},
			}, FileTree: metainfo.FileTree{
				Dir: map[string]metainfo.FileTree{
					"a": {File: metainfo.FileTreeFile{Length: 100, PiecesRoot: valid32}},
					"b": {File: metainfo.FileTreeFile{Length: 16384, PiecesRoot: valid32}},
				},
			}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := validateInfo(&c.info); err != nil {
				t.Fatalf("expected well-formed info to pass, got: %v", err)
			}
		})
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

// Regression test for a panic when adding a v2-only magnet (no v1 infohash). See
// https://github.com/anacrolix/torrent/issues/1089.
func TestAddV2OnlyMagnet(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	uri := "magnet:?xt=urn:btmh:1220" + strings.Repeat("11", 32)

	tor, err := cl.AddMagnet(uri)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsFalse(tor.infoHash.Ok))
	qt.Assert(t, qt.IsTrue(tor.infoHashV2.Ok))

	// Adding the same v2-only magnet again must be deduplicated and not panic.
	tor2, err := cl.AddMagnet(uri)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(tor, tor2))

	// Dropping and re-adding must work too.
	tor.Drop()
	tor3, err := cl.AddMagnet(uri)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(tor3.infoHashV2.Ok))
}

// Ensure hybrid (v1+v2) and pure v1 magnets are unaffected by v1-hash-optional handling.
func TestAddHybridAndV1OnlyMagnetsStillWork(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	v1Only := "magnet:?xt=urn:btih:" + strings.Repeat("11", 20)
	tor, err := cl.AddMagnet(v1Only)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(tor.infoHash.Ok))
	qt.Assert(t, qt.IsFalse(tor.infoHashV2.Ok))

	hybrid := "magnet:?xt=urn:btih:" + strings.Repeat("22", 20) +
		"&xt=urn:btmh:1220" + strings.Repeat("33", 32)
	torHybrid, err := cl.AddMagnet(hybrid)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(torHybrid.infoHash.Ok))
	qt.Assert(t, qt.IsTrue(torHybrid.infoHashV2.Ok))
}

// End-to-end: AddTorrentSpec with a v1 info whose file lengths are negative must return an error.
func TestAddTorrentSpecNegativeFileLength(t *testing.T) {
	info := metainfo.Info{
		Name:        "x",
		PieceLength: 16384,
		Pieces:      make([]byte, 20),
		Files: []metainfo.FileInfo{
			{Path: []string{"a"}, Length: -1},
			{Path: []string{"b"}, Length: 17},
		},
	}
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	_, _, err = cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  metainfo.HashBytes(infoBytes),
			InfoBytes: infoBytes,
		},
	})
	qt.Assert(t, qt.ErrorMatches(err, ".*negative file length.*"))
}

// End-to-end: a hybrid info whose v1 file lengths cover fewer pieces than its v2 file tree must be
// rejected by AddTorrentSpec. Otherwise it is accepted, and the initial piece check hashes the last
// piece, where metainfo.Piece.V1Length computes a negative length and panics the hasher goroutine,
// taking down the whole process. Reachable from a remote peer over ut_metadata after a magnet add.
func TestAddTorrentSpecHybridV1LengthTooShort(t *testing.T) {
	// The v2 file tree says file "x" is two pieces, but the v1 "length" is only one piece.
	infoBytes, err := bencode.Marshal(map[string]any{
		"name":         "x",
		"piece length": int64(16384),
		"meta version": int64(2),
		"length":       int64(16384),               // v1: one piece
		"pieces":       strings.Repeat("\x00", 40), // but two v1 piece hashes
		"file tree": map[string]any{
			"x": map[string]any{"": map[string]any{
				"length":      int64(2 * 16384), // v2: two pieces
				"pieces root": strings.Repeat("\x00", 32),
			}},
		},
	})
	qt.Assert(t, qt.IsNil(err))
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	_, _, err = cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  metainfo.HashBytes(infoBytes),
			InfoBytes: infoBytes,
		},
	})
	qt.Assert(t, qt.ErrorMatches(err, ".*v1 file lengths.*"))
}

// End-to-end: a v1 info with a zero piece length but non-empty piece hashes must be rejected when
// its info bytes are set. Otherwise it is accepted, and the initial piece check hashes the last
// piece, where metainfo.Piece.V1Length computes a zero length and panics the hasher goroutine,
// taking down the whole process. Reachable from a remote peer over ut_metadata after a magnet add,
// which is what SetInfoBytes models here.
func TestSetInfoBytesZeroPieceLength(t *testing.T) {
	infoBytes, err := bencode.Marshal(map[string]any{
		"name":         "x",
		"piece length": int64(0),
		"pieces":       strings.Repeat("\x00", 40), // two piece hashes, so two pieces
	})
	qt.Assert(t, qt.IsNil(err))
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHash: metainfo.HashBytes(infoBytes)})
	err = tt.SetInfoBytes(infoBytes)
	qt.Assert(t, qt.ErrorMatches(err, ".*zero piece length.*"))
}
