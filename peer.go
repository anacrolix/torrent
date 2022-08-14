package torrent

type peerLocalPublicAddr = IpPort

func (pc *Peer) isLowOnRequests() bool {
	return pc.requestState.Requests.IsEmpty() && pc.requestState.Cancelled.IsEmpty()
}

func (pc *Peer) decPeakRequests() {
	// // This can occur when peak requests are altered by the update request timer to be lower than
	// // the actual number of outstanding requests. Let's let it go negative and see what happens. I
	// // wonder what happens if maxRequests is not signed.
	// if p.peakRequests < 1 {
	// 	panic(p.peakRequests)
	// }
	pc.peakRequests--
}
