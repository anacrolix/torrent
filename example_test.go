package torrent_test

import (
	"context"
	"io/ioutil"
	"log"
	"net"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/sockets"
	"github.com/anacrolix/utp"
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

	if err = c.DownloadInto(context.Background(), metadata, ioutil.Discard); err != nil {
		log.Fatalln(err)
		return
	}

	log.Print("torrent downloaded")
}

func ExampleCustomNetworkProtocols() {
	var (
		err      error
		metadata torrent.Metadata
	)

	l, err := utp.NewSocket("udp", "0.0.0.0:0")
	if err != nil {
		log.Fatalln(err)
		return
	}
	defer l.Close()

	s := sockets.New(l, &net.Dialer{LocalAddr: l.Addr()})
	c, _ := torrent.NewSocketsBind(s).Bind(torrent.NewClient(torrent.NewDefaultClientConfig()))
	defer c.Close()

	if metadata, err = torrent.NewFromMagnet("magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU"); err != nil {
		log.Fatalln(err)
		return
	}

	if err = c.DownloadInto(context.Background(), metadata, ioutil.Discard); err != nil {
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
