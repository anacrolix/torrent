package metainfo

import (
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
