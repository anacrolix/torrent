package storage

import (
	"github.com/anacrolix/torrent/segments"
)

// Returns the minimum file lengths required for the given extent to exist on disk. Returns false if
// the extent is not covered by the files in the index.
func minFileLengthsForTorrentExtent(
	fileSegmentsIndex segments.Index,
	off, n int64,
	each func(fileIndex int, length int64) bool,
) bool {
	return fileSegmentsIndex.Locate(segments.Extent{
		Start:  off,
		Length: n,
	}, func(fileIndex int, segmentBounds segments.Extent) bool {
		return each(fileIndex, segmentBounds.Start+segmentBounds.Length)
	})
}
