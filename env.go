package torrent

import (
	"os"
	"strconv"

	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/exp/constraints"
)

func initIntFromEnv[T constraints.Integer](key string, defaultValue T, bitSize int) T {
	s := os.Getenv(key)
	if s == "" {
		return defaultValue
	}
	i64, err := strconv.ParseInt(s, 10, bitSize)
	panicif.Err(err)
	return T(i64)
}
