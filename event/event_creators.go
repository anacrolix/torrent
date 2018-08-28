package event

import (
	"sync"

	"github.com/anacrolix/torrent/log"
	"github.com/pkg/errors"
)

func createBaseEvent(msg string) BaseEvent {
	d := make(map[string]interface{})
	d["msg"] = msg
	return BaseEvent{&sync.Mutex{}, &d}
}

func createLeveledEvent(msg string, lvl Level) log.Event {
	evt := createBaseEvent(msg)
	evt.AddValue("level", lvl)
	return LeveledEvent{evt}
}

func createErrorEvent(msg string) log.Event {
	evt := createBaseEvent(msg)
	evt.AddValue("level", ErrorLevel)
	evt.AddValue("error", errors.WithStack(errors.New(msg)))
	return ErrorEvent{evt}
}

// Debug creates a debug level LeveledEvent
func Debug(msg string) log.Event {
	return createLeveledEvent(msg, DebugLevel)
}

// Info creates an info level LeveledEvent
func Info(msg string) log.Event {
	return createLeveledEvent(msg, InfoLevel)
}

// Error creates an ErrorEvent
func Error(msg string) log.Event {
	return createErrorEvent(msg)
}
