package event

// Level indicates what level an event is
type Level int

const (
	// DebugLevel is for marking events at Debug level
	DebugLevel = Level(iota)
	// UnknownLevel is not used for marking events level
	UnknownLevel
	// InfoLevel is for marking events at Info level
	InfoLevel
	// ErrorLevel is for marking events at Error level
	ErrorLevel
	// SilentLevel should not be used for marking events and is the highest level
	SilentLevel
)

func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case ErrorLevel:
		return "ERROR"
	default:
		return "???"
	}
}
