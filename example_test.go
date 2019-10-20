package torrent_test

import (
	"log"

	"github.com/anacrolix/torrent"
)

func Example() {
	c, _ := torrent.NewClient(nil)
	defer c.Close()
	ts, _ := torrent.NewFromMagnet("magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU")

	t, _, _ := c.Start(ts)
	<-t.GotInfo()
	t.DownloadAll()
	c.WaitAll()
	log.Print("ermahgerd, torrent downloaded")
}

func Example_fileReader() {
	var f torrent.File
	// Accesses the parts of the torrent pertaining to f. Data will be
	// downloaded as required, per the configuration of the torrent.Reader.
	r := f.NewReader()
	defer r.Close()
}
