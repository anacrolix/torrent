package torrent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
)

func TestTorrentOffsetRequest(t *testing.T) {
	check := func(tl, ps, off int64, expected Request, ok bool) {
		req, _ok := torrentOffsetRequest(tl, ps, defaultChunkSize, off)
		assert.Equal(t, _ok, ok)
		assert.Equal(t, req, expected)
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
	assert.EqualValues(t, 2+reflect.ValueOf(ConnStats{}).NumField(), lines)
}
