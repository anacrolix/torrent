# torrent

[![Codeship](https://www.codeship.io/projects/a2811d30-b0ce-0132-8983-5e604f7ebe37/status)](https://codeship.com/projects/69674)
[![GoDoc](https://godoc.org/github.com/anacrolix/torrent?status.svg)](https://godoc.org/github.com/anacrolix/torrent)

This repository implements BitTorrent-related packages and command-line utilities in Go.

There is support for protocol encryption, DHT, PEX, uTP, and various extensions. There are several storage backends provided, blob, file, mmap. You can use the provided binaries in `./cmd`, or use `torrent` as a library for your own applications.

See also the [mailing list](https://groups.google.com/forum/#!forum/go_torrent), and the [Gophers Slack channel](https://gophers.slack.com/#torrent).

## Installation

Install the library package with `go get github.com/anacrolix/torrent`, or the provided cmds with `go get github.com/anacrolix/torrent/cmd/...`.

## Library example

There is a small example in the [package documentation](https://godoc.org/github.com/anacrolix/torrent).

## Commands

Here I'll describe what some of the provided commands in `./cmd` do.

Note that [`godo`](https://bitbucket.org/anacrolix/go-utils) that I invoke in the following examples is a command that builds and executes a Go import path, like `go run`. It's easier to use this convention than to spell out the install/invoke cycle for every single example.

### torrent

Downloads torrents from the command-line.

	$ go get github.com/anacrolix/torrent/cmd/torrent
	$ torrent 'magnet:?xt=urn:btih:KRWPCX3SJUM4IMM4YF5RPHL6ANPYTQPU'
    2015/04/01 02:08:20 main.go:137: downloaded ALL the torrents
    $ md5sum ubuntu-14.04.2-desktop-amd64.iso
    1b305d585b1918f297164add46784116  ubuntu-14.04.2-desktop-amd64.iso
    $ echo such amaze
    wow

### torrentfs

torrentfs mounts a FUSE filesystem at `-mountDir`. The contents are the torrents described by the torrent files and magnet links at `-torrentPath`. Data for read requests is fetched only as required from the torrent network, and stored at `-downloadDir`.

    $ mkdir mnt torrents
    $ godo github.com/anacrolix/torrent/cmd/torrentfs -mountDir mnt -torrentPath torrents &
    $ cd torrents
    $ wget http://releases.ubuntu.com/14.04.2/ubuntu-14.04.2-desktop-amd64.iso.torrent
    $ cd ..
    $ ls mnt
    ubuntu-14.04.2-desktop-amd64.iso
    $ pv mnt/ubuntu-14.04.2-desktop-amd64.iso | md5sum
    996MB 0:04:40 [3.55MB/s] [========================================>] 100%
    1b305d585b1918f297164add46784116  -

### torrent-magnet

Creates a magnet link from a torrent file. Note the extracted trackers, display name, and info hash.

    $ godo github.com/anacrolix/torrent/cmd/torrent-magnet < ubuntu-14.04.2-desktop-amd64.iso.torrent
	magnet:?xt=urn:btih:546cf15f724d19c4319cc17b179d7e035f89c1f4&dn=ubuntu-14.04.2-desktop-amd64.iso&tr=http%3A%2F%2Ftorrent.ubuntu.com%3A6969%2Fannounce&tr=http%3A%2F%2Fipv6.torrent.ubuntu.com%3A6969%2Fannounce