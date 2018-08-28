package event

import (
	"fmt"

	"github.com/anacrolix/torrent/log"
	"github.com/pkg/errors"
)

type stackTracer interface {
	StackTrace() errors.StackTrace
}

// ErrorEvent represents an error event
type ErrorEvent struct {
	log.Event
}

// String returns formated string for this type of event
func (e ErrorEvent) String() string {
	return buildMsg(e, withLastStackFrame, withLevel)
}

// GetStackTrace returns StackTrace from event
func GetStackTrace(e log.Event) *errors.StackTrace {
	d := *e.Data()

	// Unwrap stack trace
	var ifc interface{}
	var st stackTracer
	var ok bool
	if ifc, ok = d["error"]; ok {
		st, ok = ifc.(stackTracer)
	}

	if !ok {
		return nil
	}

	trace := st.StackTrace()
	return &trace
}

func withLastStackFrame(e log.Event, s string) string {
	tp := GetStackTrace(e)

	// return original string if no stack trace present
	if tp == nil {
		return s
	}
	trace := *tp

	// Assumption that caller is 3rd frame down (created with NewErrorEvent)
	// return original string if 3rd frame is OOB
	if len(trace) < 3 {
		return s
	}

	return fmt.Sprintf("[%v] %v", trace[2], s)
}
