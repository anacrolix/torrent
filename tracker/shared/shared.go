package shared

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// I think this is necessary for when we convert a type to JSON for logging.
var _ slog.LogValuer = AnnounceEvent(0)

func (me AnnounceEvent) LogValue() slog.Value {
	return slog.StringValue(me.String())
}

// MarshalJSON implements json.Marshaler to ensure AnnounceEvent is serialized as a string in JSON
func (me AnnounceEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(me.String())
}

// UnmarshalJSON implements json.Unmarshaler to deserialize AnnounceEvent from a string in JSON
func (me *AnnounceEvent) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	return me.UnmarshalText([]byte(str))
}
