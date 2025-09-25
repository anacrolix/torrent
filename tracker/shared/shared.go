package shared

import (
	"fmt"
)

const (
	None      AnnounceEvent = iota
	Completed               // The local peer just completed the torrent.
	Started                 // The local peer has just resumed this torrent.
	Stopped                 // The local peer is leaving the swarm.
)

type AnnounceEvent int32

func (me *AnnounceEvent) UnmarshalText(text []byte) error {
	for key, str := range announceEventStrings {
		if string(text) == str {
			*me = AnnounceEvent(key)
			return nil
		}
	}
	return fmt.Errorf("unknown event")
}

var announceEventStrings = []string{"", "completed", "started", "stopped"}

func (e AnnounceEvent) String() string {
	// See BEP 3, "event", and
	// https://github.com/anacrolix/torrent/issues/416#issuecomment-751427001. Return a safe default
	// in case event values are not sanitized.
	if e < 0 || int(e) >= len(announceEventStrings) {
		return ""
	}
	return announceEventStrings[e]
}
