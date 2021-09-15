package shared

type AnnounceEvent int32

// TODO: Incorporate use of stringer for AnnounceEvent:
// go:generate stringer -type=AnnounceEvent -trimprefix=AnnounceEvent -linecomment

// NOTE: the trailing comments are the strings used in generating the `String()` method.
// See BEP 3, "event", and https://github.com/anacrolix/torrent/issues/416#issuecomment-751427001.
const (
	// Default event, equivalent to unspecified
	AnnounceEventNone AnnounceEvent = iota //
	// Local peer just completed the torrent.
	AnnouncedEventCompleted // completed
	// local peer has just resumed this torrent.
	AnnounceEventStarted    // started
	// Local peer is leaving the swarm.
	AnnounceEventStopped    // stopped
)
