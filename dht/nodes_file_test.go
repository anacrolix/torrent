package dht

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/dht/v2/krpc"
)

func TestSaveLoadNodesFile(t *testing.T) {
	f, err := ioutil.TempFile("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	f.Close()
	ns := []krpc.NodeInfo{krpc.RandomNodeInfo(4), krpc.RandomNodeInfo(16)}
	require.NoError(t, WriteNodesToFile(ns, f.Name()))
	_ns, err := ReadNodesFromFile(f.Name())
	assert.NoError(t, err)
	_ns[0].Addr.IP = _ns[0].Addr.IP.To4()
	assert.EqualValues(t, ns, _ns)
}
