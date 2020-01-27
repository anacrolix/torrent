package torrent

import (
	"fmt"
	"net"
)

// banned represents a banned peer.
type banned struct {
	IP net.IP
}

func (t banned) Error() string {
	return fmt.Sprintf("banned peer: %v", t.IP)
}
