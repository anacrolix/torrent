//go:build cgo && !disable_libutp
// +build cgo,!disable_libutp

package torrent

import (
	"log/slog"

	utp "github.com/anacrolix/go-libutp"
	"github.com/anacrolix/log"
)

func NewUtpSocketSlogger(network, addr string, fc firewallCallback, slogger *slog.Logger) (utpSocket, error) {
	// go-libutp requires an anacrolix/log.Logger, so create one bridged from the slog.Logger.
	aLogger := log.NewLogger().WithDefaultLevel(log.Debug)
	aLogger.SetHandlers(log.SlogHandlerAsHandler{SlogHandler: slogger.Handler()})
	s, err := utp.NewSocket(network, addr, utp.WithLogger(aLogger))
	if s == nil {
		return nil, err
	}
	if err != nil {
		return s, err
	}
	if fc != nil {
		s.SetSyncFirewallCallback(utp.FirewallCallback(fc))
	}
	return s, err
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
