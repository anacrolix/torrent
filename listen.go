package torrent

import "strings"

func LoopbackListenHost(network string) string {
	if strings.Contains(network, "4") {
		return "127.0.0.1"
	} else {
		// return "0:0:0:0:0:ffff:7f00:1" // 127.0.0.1 in IPv6
		return "::1"
	}
}
