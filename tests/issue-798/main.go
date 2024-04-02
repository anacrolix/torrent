package main

import (
	"fmt"

	app "github.com/anacrolix/gostdapp"
	"github.com/anacrolix/torrent"
)

func main() {
	app.Run(mainErr)
}

func mainErr() error {
	config := torrent.NewDefaultClientConfig()
	config.DataDir = "./output"
	config.ListenPort = 0
	c, err := torrent.NewClient(config)
	if err != nil {
		return err
	}
	defer c.Close()
	t, _ := c.AddMagnet("magnet:?xt=urn:btih:99c82bb73505a3c0b453f9fa0e881d6e5a32a0c1&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce")
	<-t.GotInfo()
	fmt.Println("start downloading")
	t.DownloadAll()
	c.WaitAll()
	return nil
}
