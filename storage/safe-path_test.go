package storage

import (
	"fmt"
	"log"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/torrent/metainfo"
)

func init() {
	log.SetFlags(log.Flags() | log.Lshortfile)
}

// I think these are mainly tests for bad metainfos that try to escape the client base directory.
var safeFilePathTests = []struct {
	input     []string
	expectErr bool
}{
	// We might want a test for invalid chars inside components, or file maker opt funcs returning
	// absolute paths (and thus presumably clobbering earlier "makers").
	{input: []string{"a", filepath.FromSlash(`b/..`)}, expectErr: false},
	{input: []string{"a", filepath.FromSlash(`b/../../..`)}, expectErr: true},
	{input: []string{"a", filepath.FromSlash(`b/../.././..`)}, expectErr: true},
	{
		input: []string{
			filepath.FromSlash(`NewSuperHeroMovie-2019-English-720p.avi /../../../../../Roaming/Microsoft/Windows/Start Menu/Programs/Startup/test3.exe`),
		},
		expectErr: true,
	},
}

// Tests the ToSafeFilePath func.
func TestToSafeFilePath(t *testing.T) {
	for _, _case := range safeFilePathTests {
		actual, err := ToSafeFilePath(_case.input...)
		if _case.expectErr {
			if err != nil {
				continue
			}
			t.Errorf("%q: expected error, got output %q", _case.input, actual)
		}
	}
}

// Check that safe file path handling still exists for the newer file-opt-maker variants.
func TestFileOptsSafeFilePathHandling(t *testing.T) {
	c := qt.New(t)
	for i, _case := range safeFilePathTests {
		c.Run(fmt.Sprintf("Case%v", i), func(c *qt.C) {
			info := metainfo.Info{
				Files: []metainfo.FileInfo{
					{Path: _case.input},
				},
			}
			client := NewFileOpts(NewFileClientOpts{
				ClientBaseDir: t.TempDir(),
			})
			defer func() { c.Check(client.Close(), qt.IsNil) }()
			torImpl, err := client.OpenTorrent(&info, metainfo.Hash{})
			if _case.expectErr {
				c.Check(err, qt.Not(qt.IsNil))
			} else {
				c.Check(torImpl.Close(), qt.IsNil)
			}
		})
	}
}
