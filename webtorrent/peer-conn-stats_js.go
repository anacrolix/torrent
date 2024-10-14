//go:build js && wasm
// +build js,wasm

package webtorrent

import (
	"github.com/pion/webrtc/v4"
)

// webrtc.PeerConnection.GetStats() is not currently supported for WASM. Return empty stats.
func GetPeerConnStats(pc *wrappedPeerConnection) (stats webrtc.StatsReport) {
	return
}
