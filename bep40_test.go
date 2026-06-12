package torrent

import (
	"net"
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestBep40Priority(t *testing.T) {
	qt.Check(t, qt.Equals(bep40PriorityIgnoreError(
		IpPort{IP: net.ParseIP("123.213.32.10"), Port: 0},
		IpPort{IP: net.ParseIP("98.76.54.32"), Port: 0},
	), peerPriority(0xec2d7224)))
	qt.Check(t, qt.Equals(bep40PriorityIgnoreError(
		IpPort{IP: net.ParseIP("98.76.54.32"), Port: 0},
		IpPort{IP: net.ParseIP("123.213.32.10"), Port: 0},
	), peerPriority(0xec2d7224)))
	qt.Check(t, qt.Equals(bep40PriorityIgnoreError(
		IpPort{IP: net.ParseIP("123.213.32.10"), Port: 0},
		IpPort{IP: net.ParseIP("123.213.32.234"), Port: 0},
	), peerPriority(0x99568189)))
	qt.Check(t, qt.Equals(bep40PriorityIgnoreError(
		IpPort{IP: net.ParseIP("206.248.98.111"), Port: 0},
		IpPort{IP: net.ParseIP("142.147.89.224"), Port: 0},
	), peerPriority(0x2b41d456)))
	qt.Check(t, qt.Equals(string(func() []byte {
		b, _ := bep40PriorityBytes(
			IpPort{IP: net.ParseIP("123.213.32.234"), Port: 0},
			IpPort{IP: net.ParseIP("123.213.32.234"), Port: 0},
		)
		return b
	}()), "\x00\x00\x00\x00"))
}
