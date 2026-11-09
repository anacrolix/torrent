package shared

import (
	"encoding/json"
	"testing"
)

func TestAnnounceEventJSONRoundTrip(t *testing.T) {
	for _, e := range []AnnounceEvent{None, Completed, Started, Stopped} {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal %v: %v", e, err)
		}
		var got AnnounceEvent
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != e {
			t.Fatalf("round trip: got %v, want %v", got, e)
		}
	}
}

func TestAnnounceEventOutOfRange(t *testing.T) {
	for _, e := range []AnnounceEvent{-1, 4, 100} {
		if _, err := e.MarshalText(); err == nil {
			t.Errorf("MarshalText(%d): expected error", int32(e))
		}
		if _, err := json.Marshal(e); err == nil {
			t.Errorf("json.Marshal(%d): expected error", int32(e))
		}
	}
}
