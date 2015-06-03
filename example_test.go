package torrent_test

import (
	"io"
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
	var (
		t torrent.Torrent
		f torrent.File
	)
	r := t.NewReader()
	defer r.Close()
	_ = io.NewSectionReader(r, f.Offset(), f.Length())
	// fr will read from the parts of the torrent pertaining to f.
}
