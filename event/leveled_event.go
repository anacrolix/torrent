package event

import (
	"fmt"

	"github.com/anacrolix/torrent/log"
)

// LeveledEvent is an internal event type used for log
type LeveledEvent struct {
	log.Event
}

// String returns a string which prints event msg
func (e LeveledEvent) String() string {
	return buildMsg(e, withLevel)
}

// GetEventLevel returns level of event
func GetEventLevel(e log.Event) Level {
	d := *e.Data()

	// Unwrap level, set to unknown if non-existant
	var ifc interface{}
	var lvl Level
	var ok bool
	if ifc, ok = d["level"]; ok {
		lvl, ok = ifc.(Level)
	}
	if !ok {
		return UnknownLevel
	}

	return lvl
}

func withLevel(e log.Event, s string) string {
	lvl := GetEventLevel(e)
	return fmt.Sprintf("[%v] %s", lvl, s)
}
