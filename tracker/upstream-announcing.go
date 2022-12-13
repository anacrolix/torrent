package tracker

import (
	"context"
	"time"
)

type UpstreamAnnounceGater interface {
	Start(ctx context.Context, tracker string, infoHash InfoHash,
	// How long the announce block remains before discarding it.
		timeout time.Duration,
	) (bool, error)
	Completed(
		ctx context.Context, tracker string, infoHash InfoHash,
	// Num of seconds reported by tracker, or some suitable value the caller has chosen.
		interval int32,
	) error
}
