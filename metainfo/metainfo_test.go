package metainfo

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anacrolix/missinggo/v2"
	"github.com/davecgh/go-spew/spew"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/bencode"
)

func testFile(t *testing.T, filename string) {
	mi, err := LoadFromFile(filename)
	require.NoError(t, err)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)

	if len(info.Files) == 1 {
		t.Logf("Single file: %s (length: %d)\n", info.BestName(), info.Files[0].Length)
	} else {
		t.Logf("Multiple files: %s\n", info.BestName())
		for _, f := range info.Files {
			t.Logf(" - %s (length: %d)\n", path.Join(f.Path...), f.Length)
		}
	}

	for _, group := range mi.AnnounceList {
		for _, tracker := range group {
			t.Logf("Tracker: %s\n", tracker)
		}
	}

	b, err := bencode.Marshal(&info)
	require.NoError(t, err)
	assert.EqualValues(t, string(b), string(mi.InfoBytes))
}

func TestFile(t *testing.T) {
	testFile(t, "testdata/archlinux-2011.08.19-netinstall-i686.iso.torrent")
	testFile(t, "testdata/continuum.torrent")
	testFile(t, "testdata/23516C72685E8DB0C8F15553382A927F185C4F01.torrent")
	testFile(t, "testdata/trackerless.torrent")
	_, err := LoadFromFile("testdata/minimal-trailing-newline.torrent")
	c := qt.New(t)
	c.Check(err, qt.ErrorMatches, ".*expected EOF")
}

// Ensure that the correct number of pieces are generated when hashing files.
func TestNumPieces(t *testing.T) {
	for _, _case := range []struct {
		PieceLength int64
		Files       []FileInfo
		NumPieces   int
	}{
		{256 * 1024, []FileInfo{{Length: 1024*1024 + -1}}, 4},
		{256 * 1024, []FileInfo{{Length: 1024 * 1024}}, 4},
		{256 * 1024, []FileInfo{{Length: 1024*1024 + 1}}, 5},
		{5, []FileInfo{{Length: 1}, {Length: 12}}, 3},
		{5, []FileInfo{{Length: 4}, {Length: 12}}, 4},
	} {
		info := Info{
			Files:       _case.Files,
			PieceLength: _case.PieceLength,
		}
		err := info.GeneratePieces(func(fi FileInfo) (io.ReadCloser, error) {
			return io.NopCloser(missinggo.ZeroReader), nil
		})
		assert.NoError(t, err)
		assert.EqualValues(t, _case.NumPieces, info.NumPieces())
	}
}

func touchFile(path string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	err = f.Close()
	return
}

func TestBuildFromFilePathOrder(t *testing.T) {
	td := t.TempDir()
	require.NoError(t, touchFile(filepath.Join(td, "b")))
	require.NoError(t, touchFile(filepath.Join(td, "a")))
	info := Info{
		PieceLength: 1,
	}
	require.NoError(t, info.BuildFromFilePath(td))
	assert.EqualValues(t, []FileInfo{{
		Path: []string{"a"},
	}, {
		Path: []string{"b"},
	}}, info.Files)
}

func testUnmarshal(t *testing.T, input string, expected *MetaInfo) {
	var actual MetaInfo
	err := bencode.Unmarshal([]byte(input), &actual)
	if expected == nil {
		assert.Error(t, err)
		return
	}
	assert.NoError(t, err)
	assert.EqualValues(t, *expected, actual)
}

func TestUnmarshal(t *testing.T) {
	testUnmarshal(t, `de`, &MetaInfo{})
	testUnmarshal(t, `d4:infoe`, nil)
	testUnmarshal(t, `d4:infoabce`, nil)
	testUnmarshal(t, `d4:infodee`, &MetaInfo{InfoBytes: []byte("de")})
}

func TestMetainfoWithListURLList(t *testing.T) {
	mi, err := LoadFromFile("testdata/SKODAOCTAVIA336x280_archive.torrent")
	require.NoError(t, err)
	assert.Len(t, mi.UrlList, 3)
	qt.Assert(t, mi.Magnet(nil, nil).String(), qt.ContentEquals,
		strings.Join([]string{
			"magnet:?xt=urn:btih:d4b197dff199aad447a9a352e31528adbbd97922",
			"tr=http%3A%2F%2Fbt1.archive.org%3A6969%2Fannounce",
			"tr=http%3A%2F%2Fbt2.archive.org%3A6969%2Fannounce",
			"ws=https%3A%2F%2Farchive.org%2Fdownload%2F",
			"ws=http%3A%2F%2Fia601600.us.archive.org%2F26%2Fitems%2F",
			"ws=http%3A%2F%2Fia801600.us.archive.org%2F26%2Fitems%2F",
		}, "&"))
}

func TestMetainfoWithStringURLList(t *testing.T) {
	mi, err := LoadFromFile("testdata/flat-url-list.torrent")
	require.NoError(t, err)
	assert.Len(t, mi.UrlList, 1)
	qt.Assert(t, mi.Magnet(nil, nil).String(), qt.ContentEquals,
		strings.Join([]string{
			"magnet:?xt=urn:btih:9da24e606e4ed9c7b91c1772fb5bf98f82bd9687",
			"tr=http%3A%2F%2Fbt1.archive.org%3A6969%2Fannounce",
			"tr=http%3A%2F%2Fbt2.archive.org%3A6969%2Fannounce",
			"ws=https%3A%2F%2Farchive.org%2Fdownload%2F",
		}, "&"))
}

// https://github.com/anacrolix/torrent/issues/247
//
// The decoder buffer wasn't cleared before starting the next dict item after
// a syntax error on a field with the ignore_unmarshal_type_error tag.
func TestStringCreationDate(t *testing.T) {
	var mi MetaInfo
	assert.NoError(t, bencode.Unmarshal([]byte("d13:creation date23:29.03.2018 22:18:14 UTC4:infodee"), &mi))
}

// See https://github.com/anacrolix/torrent/issues/843.
func TestUnmarshalEmptyStringNodes(t *testing.T) {
	var mi MetaInfo
	c := qt.New(t)
	err := bencode.Unmarshal([]byte("d5:nodes0:e"), &mi)
	c.Assert(err, qt.IsNil)
}

func TestUnmarshalV2Metainfo(t *testing.T) {
	c := qt.New(t)
	mi, err := LoadFromFile("../testdata/bittorrent-v2-test.torrent")
	c.Assert(err, qt.IsNil)
	info, err := mi.UnmarshalInfo()
	c.Assert(err, qt.IsNil)
	spew.Dump(info)
	c.Check(info.NumPieces(), qt.Not(qt.Equals), 0)
	err = ValidatePieceLayers(mi.PieceLayers, &info.FileTree, info.PieceLength)
	c.Check(err, qt.IsNil)
}
