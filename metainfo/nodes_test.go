package metainfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodesListStrings(t *testing.T) {
	mi, err := LoadFromFile("testdata/trackerless.torrent")
	require.NoError(t, err)
	assert.EqualValues(t, []string{
		"udp://tracker.openbittorrent.com:80",
		"udp://tracker.openbittorrent.com:80",
	}, mi.Nodes)
}

func TestNodesListPairsBEP5(t *testing.T) {
}
