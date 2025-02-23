package dht

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent/dht/krpc"
)

func TestSaveLoadNodesFile(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	f.Close()
	ns := []krpc.NodeInfo{krpc.RandomNodeInfo(4), krpc.RandomNodeInfo(16)}
	require.NoError(t, WriteNodesToFile(ns, f.Name()))
	_ns, err := ReadNodesFromFile(f.Name())
	assert.NoError(t, err)
	_ns[0].Addr = krpc.NewNodeAddrFromIPPort(_ns[0].Addr.IP().To4(), int(_ns[0].Addr.Port()))
	assert.EqualValues(t, ns, _ns)
}
