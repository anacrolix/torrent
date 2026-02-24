package shared

import (
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
)

const (
	None      AnnounceEvent = iota
	Completed               // The local peer just completed the torrent.
	Started                 // The local peer has just resumed this torrent.
	Stopped                 // The local peer is leaving the swarm.
)

type AnnounceEvent int32

func (me *AnnounceEvent) UnmarshalText(text []byte) error {
	i := slices.Index(announceEventStrings, string(text))
	if i == -1 {
		return fmt.Errorf("unknown announce event string: %q", text)
	}
	*me = AnnounceEvent(i)
	return nil
}

func (me AnnounceEvent) MarshalText() ([]byte, error) {
	return []byte(announceEventStrings[me]), nil
}

var announceEventStrings = []string{"", "completed", "started", "stopped"}

func (e AnnounceEvent) String() string {
	// See BEP 3, "event", and
	// https://github.com/anacrolix/torrent/issues/416#issuecomment-751427001. Return a safe default
	// in case event values are not sanitized.
	if e < 0 || int(e) >= len(announceEventStrings) {
		return fmt.Sprintf("<unknown announce event %d>", e)
	}
	s := announceEventStrings[e]
	if e == None && s == "" {
		s = "<regular>"
	}
	return s
}

// I think this is necessary for when we convert a type to JSON for logging.
var _ interface {
	slog.LogValuer
	encoding.TextMarshaler
} = None

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
