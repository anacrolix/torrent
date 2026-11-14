package torrent

import (
	"net/netip"

	g "github.com/anacrolix/generics"
)

// Reports whether addr is only meaningful on the local host or a local network.
func isNonGlobalAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsValid() ||
		addr.IsUnspecified() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast()
}

// Reports whether a peer at source may have us dial target, such as through PEX or ut_holepunch.
// Peers that aren't themselves on a local network can't direct us to local addresses.
func peerMayDirectDialTo(source g.Option[netip.Addr], target netip.Addr) bool {
	if !isNonGlobalAddr(target) {
		return true
	}
	return source.Ok && isNonGlobalAddr(source.Value)
}
