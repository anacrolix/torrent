package torrent

import (
	"io"
	"io/ioutil"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/missinggo/bitmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelRequestOptimized(t *testing.T) {
	r, w := io.Pipe()
	c := &connection{
		PeerMaxRequests: 1,
		peerPieces: func() bitmap.Bitmap {
			var bm bitmap.Bitmap
			bm.Set(1, true)
			return bm
		}(),
		rw: struct {
			io.Reader
			io.Writer
		}{
			Writer: w,
		},
		conn: new(net.TCPConn),
		// For the locks
		t: &Torrent{cl: &Client{}},
	}
	assert.Len(t, c.Requests, 0)
	c.Request(newRequest(1, 2, 3))
	require.Len(t, c.Requests, 1)
	// Posting this message should removing the pending Request.
	require.True(t, c.Cancel(newRequest(1, 2, 3)))
	assert.Len(t, c.Requests, 0)
	// Check that write optimization filters out the Request, due to the
	// Cancel. We should have received an Interested, due to the initial
	// request, and then keep-alives until we close the connection.
	go c.writer(0)
	b := make([]byte, 9)
	n, err := io.ReadFull(r, b)
	require.NoError(t, err)
	require.EqualValues(t, len(b), n)
	require.EqualValues(t, "\x00\x00\x00\x01\x02"+"\x00\x00\x00\x00", string(b))
	time.Sleep(time.Millisecond)
	c.mu().Lock()
	c.Close()
	c.mu().Unlock()
	w.Close()
	b, err = ioutil.ReadAll(r)
	require.NoError(t, err)
	// A single keep-alive will have gone through, as writer would be stuck
	// trying to flush it, and then promptly close.
	require.EqualValues(t, "\x00\x00\x00\x00", string(b))
}
