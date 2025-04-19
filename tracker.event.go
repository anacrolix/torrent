package torrent

import (
	"context"
	"log"
	"time"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/errorsx"

	"github.com/james-lawrence/torrent/tracker"
)

const ErrNoPeers = errorsx.String("failed to locate any peers for torrent")

func TrackerEvent(ctx context.Context, l Torrent) (ret *tracker.AnnounceResponse, err error) {
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
		TuneReadAnnounce(&announcer),
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

	return &res, err
}

func TrackerAnnounceOnce(ctx context.Context, l Torrent) (peers Peers, err error) {
	ctx, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()

	announced, err := TrackerEvent(ctx, l)
	if err != nil {
		log.Println("announce failed", err)
		return nil, err
	}

	if len(announced.Peers) == 0 {
		return nil, ErrNoPeers
	}

	return peers.AppendFromTracker(announced.Peers), nil
}
