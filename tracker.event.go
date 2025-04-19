package torrent

import (
	"context"
	"time"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/pkg/errors"

	"github.com/james-lawrence/torrent/tracker"
)

const ErrNoPeers = errorsx.String("failed to locate any peers for torrent")

func TrackerEvent(ctx context.Context, l Torrent, announceuri string) (ret *tracker.AnnounceResponse, err error) {
	var (
		announcer tracker.Announce
		port      int
		s         Stats
		id        int160.T
		infoid    int160.T
	)

	if err = l.Tune(
		TuneResetTrackingStats(&s),
		TuneReadPeerID(&id),
		TuneReadHashID(&infoid),
		TuneReadAnnounce(&announcer, announceuri),
		TuneReadPort(&port),
	); err != nil {
		return nil, err
	}

	req := tracker.NewAccounceRequest(
		id,
		port,
		infoid,
		tracker.AnnounceOptionKey,
		tracker.AnnounceOptionDownloaded(s.BytesReadUsefulData.Int64()),
		tracker.AnnounceOptionUploaded(s.BytesWrittenData.n),
	)

	res, err := announcer.Do(ctx, req)
	return &res, errors.Wrapf(err, "announce: %s", announcer.TrackerUrl)
}

func TrackerAnnounceOnce(ctx context.Context, l Torrent, uri string) (delay time.Duration, peers Peers, err error) {
	ctx, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()

	announced, err := TrackerEvent(ctx, l, uri)
	if err != nil {
		return delay, nil, err
	}

	if d := time.Duration(announced.Interval) * time.Second; delay < d {
		delay = d
	}

	if len(announced.Peers) == 0 {
		return delay, nil, ErrNoPeers
	}

	return delay, peers.AppendFromTracker(announced.Peers), nil
}
