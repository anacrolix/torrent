package torrent_test

import (
	"context"
	"io/ioutil"
	"log"

	"github.com/anacrolix/torrent"
)

func ExampleDownload() {
	var (
		err      error
		metadata torrent.Metadata
	)

	c, _ := torrent.NewDefaultClient()
	defer c.Close()

	if metadata, err = torrent.NewFromMagnet("magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU"); err != nil {
		log.Fatalln(err)
		return
	}

	if err = c.Download(context.Background(), metadata, ioutil.Discard); err != nil {
		log.Fatalln(err)
		return
	}

	log.Print("torrent downloaded")
}

func ExampleFileReader() {
	var f torrent.File
	// Accesses the parts of the torrent pertaining to f. Data will be
	// downloaded as required, per the configuration of the torrent.Reader.
	r := f.NewReader()
	defer r.Close()
}
