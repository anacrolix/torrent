package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/anacrolix/torrent/metainfo"
)

func TestExtentCompleteRequiredLengths(t *testing.T) {
	info := &metainfo.InfoEx{
		Info: metainfo.Info{
			Files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 2},
				{Path: []string{"b"}, Length: 3},
			},
		},
	}
	assert.Empty(t, extentCompleteRequiredLengths(&info.Info, 0, 0))
	assert.EqualValues(t, []metainfo.FileInfo{
		{Path: []string{"a"}, Length: 1},
	}, extentCompleteRequiredLengths(&info.Info, 0, 1))
	assert.EqualValues(t, []metainfo.FileInfo{
		{Path: []string{"a"}, Length: 2},
	}, extentCompleteRequiredLengths(&info.Info, 0, 2))
	assert.EqualValues(t, []metainfo.FileInfo{
		{Path: []string{"a"}, Length: 2},
		{Path: []string{"b"}, Length: 1},
	}, extentCompleteRequiredLengths(&info.Info, 0, 3))
	assert.EqualValues(t, []metainfo.FileInfo{
		{Path: []string{"b"}, Length: 2},
	}, extentCompleteRequiredLengths(&info.Info, 2, 2))
	assert.EqualValues(t, []metainfo.FileInfo{
		{Path: []string{"b"}, Length: 3},
	}, extentCompleteRequiredLengths(&info.Info, 4, 1))
	assert.Len(t, extentCompleteRequiredLengths(&info.Info, 5, 0), 0)
	assert.Panics(t, func() { extentCompleteRequiredLengths(&info.Info, 6, 1) })

}
