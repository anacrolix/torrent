package bep44

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWrapper(t *testing.T) {
	require := require.New(t)
	w := NewWrapper(NewMemory(), 10*time.Hour)

	i, err := NewItem([]byte("Hello World!"), nil, 0, 0, nil)
	require.NoError(err)

	err = w.Put(i)
	require.NoError(err)

	target := i.Target()

	targetStr := hex.EncodeToString(target[:])
	require.Equal("e5f96f6f38320f0f33959cb4d3d656452117aadb", targetStr)

	i2, err := w.Get(target)
	require.NoError(err)
	require.Equal(i, i2)
}

func TestWrapperTimeout(t *testing.T) {
	require := require.New(t)
	w := NewWrapper(NewMemory(), 0*time.Second)

	i, err := NewItem([]byte("Hello World!"), nil, 0, 0, nil)
	require.NoError(err)

	err = w.Put(i)
	require.NoError(err)
	_, err = w.Get(i.Target())
	require.Equal(ErrItemNotFound, err)
}
