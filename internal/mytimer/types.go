package mytimer

import (
	"time"
)

// The timer deadline. Zero value is a special case it means it won't fire at all.
type TimeValue struct {
	time.Time
}

func FromTime(t time.Time) TimeValue {
	return TimeValue{t}
}

func Immediate() TimeValue {
	// Could just return zero+1...
	return TimeValue{time.Now()}
}

func Never() (_ TimeValue) {
	return
}

// What daemon have I raised?
func bothEither(a, b bool) (both, either bool) {
	return b && a, b || a
}

// The values are effectively the same. That is due to the current time, resetting a Timer with
// either of them would have the same behaviour.
func effectivelyEq(a, b TimeValue) bool {
	if both, either := bothEither(a.IsZero(), b.IsZero()); either {
		return both
	}
	now := time.Now()
	if both, either := bothEither(a.Compare(now) <= 0, b.Compare(now) <= 0); either {
		return both
	}
	return a.Equal(b.Time)
}

type Func = func() TimeValue
