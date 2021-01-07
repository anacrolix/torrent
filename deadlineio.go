package torrent

import (
	"fmt"
	"io"
	"net"
	"time"
)

// Wraps a raw connection and provides the interface we want for using the
// connection in the message loop.
type deadlineReader struct {
	nc net.Conn
	r  io.Reader
}

func (r deadlineReader) Read(b []byte) (int, error) {
	// Keep-alives should be received every 2 mins. Give a bit of gracetime.
	err := r.nc.SetReadDeadline(time.Now().Add(150 * time.Second))
	if err != nil {
		return 0, fmt.Errorf("error setting read deadline: %s", err)
	}
	return r.r.Read(b)
}
