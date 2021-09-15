package shared

type AnnounceEvent int32

const (
	AnnounceEventNone AnnounceEvent = iota      //
	
	// Local peer just completed the torrent.
	AnnouncedEventCompleted                     // completed
	// local peer has just resumed this torrent.
	AnnounceEventStarted                        // started
	// Local peer is leaving the swarm.
	AnnounceEventStopped                        // stopped
)

// TODO: Incorporate use of stringer for AnnounceEvent:
// go:generate stringer -type=AnnounceEvent -trimprefix=AnnounceEvent -linecomment
