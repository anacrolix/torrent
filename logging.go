package torrent

import (
	"fmt"
	"io"
	"log"
)

// logger is a logger interface compatible with both stdlib and some
// 3rd party loggers.
type logger interface {
	Output(int, string) error
}

// implements the additional methods used by the package for logging.
type llog struct {
	logger
}

// Println replicates the behaviour of the standard logger.
func (t llog) Println(v ...interface{}) {
	t.Output(2, fmt.Sprintln(v...))
}

// Printf replicates the behaviour of the standard logger.
func (t llog) Printf(format string, v ...interface{}) {
	t.Output(2, fmt.Sprintf(format, v...))
}

// Print replicates the behaviour of the standard logger.
func (t llog) Print(v ...interface{}) {
	t.Output(2, fmt.Sprint(v...))
}

type discard struct{}

func (discard) Output(int, string) error {
	return nil
}

type logoutput interface {
	Writer() io.Writer
}

// if possible use the provided logger as a base, otherwise fallback to the default
func newlogger(l logger, prefix string, flags int) *log.Logger {
	if l, ok := l.(logoutput); ok {
		return log.New(l.Writer(), prefix, flags)
	}

	return log.New(io.Discard, prefix, log.Flags())
}
