package log

import "fmt"

// Event is a internal system event which will be logged
type Event interface {
	fmt.Stringer
	Data() *map[string]interface{}
}

// Logger is an interface to a something that can handle logging of events
type Logger interface {
	Log(Event)
}
