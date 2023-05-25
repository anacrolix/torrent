package torrent

import (
	"io"

	"github.com/davecgh/go-spew/spew"
)

func dumpStats[T any](w io.Writer, stats T) {
	spew.NewDefaultConfig()
	spew.Fdump(w, stats)
}
