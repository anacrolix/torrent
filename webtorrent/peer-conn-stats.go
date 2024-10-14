//go:build !js
// +build !js

package webtorrent

import (
	"github.com/pion/webrtc/v4"
)

func GetPeerConnStats(pc *wrappedPeerConnection) (stats webrtc.StatsReport) {
	if pc != nil {
		stats = pc.GetStats()
	}
	return
}
