package torrent

import (
	"iter"

	g "github.com/anacrolix/generics"
)

// Returns Some of the last item in a iter.Seq, or None if the sequence is empty.
func seqLast[V any](seq iter.Seq[V]) (last g.Option[V]) {
	for item := range seq {
		last.Set(item)
	}
	return
}
