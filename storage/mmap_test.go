package storage

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/torrent/internal/testutil"
)

func TestMmapWindows(t *testing.T) {
	c := qt.New(t)
	dir, mi := testutil.GreetingTestTorrent()
	cs := NewMMap(dir)
	defer func() {
		c.Check(cs.Close(), qt.IsNil)
	}()
	info, err := mi.UnmarshalInfo()
	c.Assert(err, qt.IsNil)
	ts, err := cs.OpenTorrent(&info, mi.HashInfoBytes())
	c.Assert(err, qt.IsNil)
	defer func() {
		c.Check(ts.Close(), qt.IsNil)
	}()
}
