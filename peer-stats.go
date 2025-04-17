package torrent

type PeerStats struct {
	ConnStats

	DownloadRate        float64
	LastWriteUploadRate float64
	// How many pieces the peer has.
	RemotePieceCount int
}
