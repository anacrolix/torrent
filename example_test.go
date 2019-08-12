package torrent_test

import (
	"log"

	"github.com/anacrolix/torrent"
)

func Example() {
	c, _ := torrent.NewClient(nil)
	defer c.Close()
	t, _ := c.AddMagnet("magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU")
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
