package torrent

import (
	"net"
)

// isLocalIp reports whether ip is on the same host or a local network: loopback, link-local, or a
// private range (RFC 1918, RFC 4193). These are the addresses another client on the same machine or
// LAN would be reached at, including peers found by BEP 14 Local Service Discovery.
func isLocalIp(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func (me PeerInfo) isLocal() bool {
	return isLocalIp(me.addr().IP)
}

func (c *Peer) isLocal() bool {
	return isLocalIp(c.remoteIpPort().IP)
}

func (t *Torrent) prioritizeLocalPeers() bool {
	return t.cl.config.PrioritizeLocalPeers
}

// localPeerMayStealRequest reports whether stealer should take a request from holder regardless of
// how many requests each has outstanding. ok is false when locality doesn't decide it.
func (t *Torrent) localPeerMayStealRequest(stealer, holder *Peer) (steal, ok bool) {
	if !t.prioritizeLocalPeers() {
		return
	}
	stealerLocal := stealer.isLocal()
	if stealerLocal == holder.isLocal() {
		return
	}
	return stealerLocal, true
}
