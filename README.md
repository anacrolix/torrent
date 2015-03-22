# torrent

[![Codeship](https://www.codeship.io/projects/a2811d30-b0ce-0132-8983-5e604f7ebe37/status)](https://codeship.com/projects/69674)
[![GoDoc](https://godoc.org/github.com/anacrolix/torrent?status.svg)](https://godoc.org/github.com/anacrolix/torrent)

This repository implements BitTorrent-related packages and command-line utilities in Go.

There is support for protocol encryption, DHT, PEX, uTP, and various extensions. There are several storage backends provided, blob, file, mmap. You can use the provided binaries in `./cmd`, or use `torrent` as a library for your own applications.

## Installation

Install the library package with `go get github.com/anacrolix/torrent`, or the provided cmds with `go get github.com/anacrolix/torrent/cmd/...`.

## Library example

There is a small example in the [package documentation](https://godoc.org/github.com/anacrolix/torrent).

## Torrent utility

There's a provided utility that downloads torrents from the command-line.

	$ go get github.com/anacrolix/torrent/cmd/torrent
	$ torrent 'magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU'
    2015/03/20 22:51:41 main.go:96: downloaded ALL the torrents
    $ md5sum ubuntu-14.04.1-desktop-amd64.iso
    119cb63b48c9a18f31f417f09655efbd  ubuntu-14.04.1-desktop-amd64.iso
    $ echo such amaze
    wow
