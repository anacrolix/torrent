package torrent

import (
	"context"
	"log"

	"github.com/davecgh/go-spew/spew"
	"github.com/james-lawrence/torrent/dht/int160"

	"github.com/james-lawrence/torrent/tracker"
)

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

	log.Println("announcing", announcer.TrackerUrl, spew.Sdump(req))
	res, err := announcer.Do(ctx, req)

	return &res, err
}
