package test

import (
	"io"
	"log"
	"net"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/dialer"
)

func TestUnixConns(t *testing.T) {
	var closers []io.Closer
	defer func() {
		for _, c := range closers {
			c.Close()
		}
	}()
	configure := ConfigureClient{
		Config: func(cfg *torrent.ClientConfig) {
			cfg.DisableUTP = true
			cfg.DisableTCP = true
			cfg.Debug = true
		},
		Client: func(cl *torrent.Client) {
			cl.AddDialer(torrent.NetworkDialer{Network: "unix", Dialer: dialer.Default})
			l, err := net.Listen("unix", filepath.Join(t.TempDir(), "socket"))
			if err != nil {
				panic(err)
			}
			log.Printf("created listener %q", l)
			closers = append(closers, l)
			cl.AddListener(l)
		},
	}
	testClientTransfer(t, testClientTransferParams{
		ConfigureSeeder:  configure,
		ConfigureLeecher: configure,
	})
}
