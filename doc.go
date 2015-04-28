/*
Package torrent implements a torrent client.

Simple example:

	c, _ := torrent.NewClient(&torrent.Config{})
	defer c.Close()
	t, _ := c.AddMagnet("magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU")
	<-t.GotInfo
	t.DownloadAll()
	c.WaitAll()
	log.Print("ermahgerd, torrent downloaded")


*/
package torrent
