package leveled_logger

import (
	stdLog "log"
	"os"

	"github.com/anacrolix/torrent/event"
	"github.com/anacrolix/torrent/log"
)

// LeveledLogger is a simple logger type that just filters by level
type LeveledLogger struct {
	logger   *stdLog.Logger
	logLevel *event.Level
}

// Log filters by log level and prints logs
func (l LeveledLogger) Log(e log.Event) {
	lvl := event.GetEventLevel(e)
	if lvl < *l.logLevel {
		return
	}

	l.logger.Println(e)
}

// NewLeveledLogger creates a leveled logger
func NewLeveledLogger(lvl event.Level) LeveledLogger {
	return LeveledLogger{stdLog.New(os.Stderr, "", stdLog.LstdFlags), &lvl}
}
