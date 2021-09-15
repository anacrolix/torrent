// Code generated (partially) by "stringer -type=AnnounceEvent -trimprefix=AnnounceEvent -linecomment"; DO NOT EDIT.

package shared

const (
	// "constant overflows uint" compiler error indicates constant values have changed.
	// Re-run stringer to generate them again.
	_ = -uint(AnnounceEventNone-0)
	_ = -uint(AnnouncedEventCompleted-1)
	_ = -uint(AnnounceEventStarted-2)
	_ = -uint(AnnounceEventStopped-3)
)

const _AnnounceEvent_name = "completedstartedstopped"

var _AnnounceEvent_index = [...]uint8{0, 0, 9, 16, 23}

func (i AnnounceEvent) String() string {
	if i < 0 || i >= AnnounceEvent(len(_AnnounceEvent_index)-1) {
		panic("")
	}
	return _AnnounceEvent_name[_AnnounceEvent_index[i]:_AnnounceEvent_index[i+1]]
}
