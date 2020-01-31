package torrent

import "fmt"

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
