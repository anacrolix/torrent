package torrent

import (
	"os"
	"strconv"
	"time"

	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/exp/constraints"
)

func initIntFromEnv[T constraints.Signed](key string, defaultValue T, bitSize int) T {
	return strconvFromEnv(key, defaultValue, bitSize, strconv.ParseInt)
}

func initUIntFromEnv[T constraints.Unsigned](key string, defaultValue T, bitSize int) T {
	return strconvFromEnv(key, defaultValue, bitSize, strconv.ParseUint)
}

func strconvFromEnv[T, U constraints.Integer](key string, defaultValue T, bitSize int, conv func(s string, base, bitSize int) (U, error)) T {
	s := os.Getenv(key)
	if s == "" {
		return defaultValue
	}
	i64, err := conv(s, 10, bitSize)
	panicif.Err(err)
	return T(i64)
}

// initDurationFromEnv reads a time.ParseDuration-formatted value from the environment,
// falling back to defaultValue when the variable is unset or empty. Like the numeric
// helpers above it panics on a malformed value rather than silently using the default:
// these variables exist to be swept in experiments, and a typo that quietly reverts to
// the default arm produces a measurement that looks valid and is not.
func initDurationFromEnv(key string, defaultValue time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(s)
	panicif.Err(err)
	return d
}
