package torrent

type Config struct {
	DataDir          string
	ListenAddr       string
	DisableTrackers  bool
	DownloadStrategy DownloadStrategy
	NoDHT            bool
	NoUpload         bool
}
