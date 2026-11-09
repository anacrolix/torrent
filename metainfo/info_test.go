package metainfo

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
)

func TestMarshalInfo(t *testing.T) {
	var info Info
	g.MakeSliceWithLength(&info.Pieces, 0)
	b, err := bencode.Marshal(info)
	qt.Check(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(b), "d4:name0:12:piece lengthi0e6:pieces0:e"))
}

func TestFileTreeValidate(t *testing.T) {
	valid32 := strings.Repeat("x", 32)

	for _, tc := range []struct {
		name    string
		tree    FileTree
		wantErr bool
	}{
		{
			name: "ValidNonEmptyFile",
			tree: FileTree{
				Dir: map[string]FileTree{
					"a": {File: FileTreeFile{Length: 1024, PiecesRoot: valid32}},
				},
			},
		},
		{
			name: "EmptyFileNoRoot",
			tree: FileTree{
				Dir: map[string]FileTree{
					"a": {File: FileTreeFile{Length: 0, PiecesRoot: ""}},
				},
			},
		},
		{
			name:    "ShortRoot",
			wantErr: true,
			tree: FileTree{
				Dir: map[string]FileTree{
					"a": {File: FileTreeFile{Length: 1, PiecesRoot: "short"}},
				},
			},
		},
		{
			name:    "LongRoot",
			wantErr: true,
			tree: FileTree{
				Dir: map[string]FileTree{
					"a": {File: FileTreeFile{Length: 1, PiecesRoot: strings.Repeat("x", 64)}},
				},
			},
		},
		{
			name:    "MissingRootNonEmptyFile",
			wantErr: true,
			tree: FileTree{
				Dir: map[string]FileTree{
					"a": {File: FileTreeFile{Length: 100, PiecesRoot: ""}},
				},
			},
		},
		{
			name:    "EmptyFileWithRoot",
			wantErr: true,
			tree: FileTree{
				Dir: map[string]FileTree{
					"a": {File: FileTreeFile{Length: 0, PiecesRoot: valid32}},
				},
			},
		},
		{
			name:    "NestedInvalidEntry",
			wantErr: true,
			tree: FileTree{
				Dir: map[string]FileTree{
					"dir": {
						Dir: map[string]FileTree{
							"good": {File: FileTreeFile{Length: 1, PiecesRoot: valid32}},
							"bad":  {File: FileTreeFile{Length: 1, PiecesRoot: "short"}},
						},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tree.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected validation error")
				}
				t.Logf("got expected error: %v", err)
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

// A symlink to a regular file is recorded with the target's length, which is
// what GeneratePieces hashes; Walk's Lstat size (the link's own) must not
// leak into the torrent.
func TestBuildFromFilePathFollowsFileSymlinks(t *testing.T) {
	realDir, linkDir := t.TempDir(), t.TempDir()
	target := filepath.Join(realDir, "payload.bin")
	if err := os.WriteFile(target, bytes.Repeat([]byte("x"), 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(linkDir, "payload.bin")); err != nil {
		t.Skip(err)
	}
	var overLinks, overReal Info
	if err := overLinks.BuildFromFilePath(linkDir); err != nil {
		t.Fatal(err)
	}
	if err := overReal.BuildFromFilePath(realDir); err != nil {
		t.Fatal(err)
	}
	if got := overLinks.UpvertedFiles()[0].Length; got != 5000 {
		t.Fatalf("length through symlink = %d, want 5000", got)
	}
	if !bytes.Equal(overLinks.Pieces, overReal.Pieces) {
		t.Fatal("pieces differ between the linked and the real tree")
	}
	// A symlink to a directory is refused rather than silently mis-sized.
	if err := os.Symlink(realDir, filepath.Join(linkDir, "dir")); err == nil {
		var info Info
		if err := info.BuildFromFilePath(linkDir); err == nil {
			t.Fatal("symlink to a directory accepted")
		}
	}
}
