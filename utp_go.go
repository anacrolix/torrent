//go:build !cgo || disable_libutp
// +build !cgo disable_libutp

package torrent

import (
	"log/slog"

	"github.com/anacrolix/log"
	"github.com/anacrolix/utp"
)

func NewUtpSocketSlogger(network, addr string, _ firewallCallback, _ *slog.Logger) (utpSocket, error) {
	s, err := utp.NewSocket(network, addr)
	if s == nil {
		return nil, err
	} else {
		return s, err
	}
}

// Deprecated: Use [NewUtpSocketSlogger].
func NewUtpSocket(network, addr string, fc firewallCallback, logger log.Logger) (utpSocket, error) {
	var sl *slog.Logger
	if !logger.IsZero() {
		sl = logger.Slogger()
	} else {
		sl = slog.Default()
	}
	return NewUtpSocketSlogger(network, addr, fc, sl)
}
