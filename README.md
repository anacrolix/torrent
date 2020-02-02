# torrent

[![GoDoc](https://godoc.org/github.com/james-lawrence/torrent?status.svg)](https://godoc.org/github.com/james-lawrence/torrent)

This repository is a refactor of [anacrolix's](https://github.com/anacrolix/torrent), to simplify the API of the library,
use more idiomatic code, improve horizontal scalability, remove extraneous dependencies, and to add in some extended functionality.

## improvements implemented by library
- smaller API surface. simplifying use, while still maintaining almost full functionality provided (some read ahead logic is broken, which is risky anyways due to the data being unvalidated).
- refactored the single lock used for all torrents out. this means the torrents do not contend with each other for the singe lock.
- removed a number of panic making the code safer to use (more to do here).
- ability to use arbitrary socket implementations.

## improvements planned
- performance enhancements.

## Installation

Install the library package with `go get -u github.com/james-lawrence/torrent`, or the provided cmds with `go get -u github.com/james-lawrence/torrent/cmd/...`.

## Library examples

There are some small [examples](https://godoc.org/github.com/james-lawrence/torrent#pkg-examples) in the package documentation.

## Help

Communication about the project is through the [issue tracker](https://github.com/james-lawrence/torrent/issues).

### torrent

Downloads torrents from the command-line. This first example does not use `godo`.

	$ go get github.com/james-lawrence/torrent/cmd/torrent
    # Now 'torrent' should be in $GOPATH/bin, which should be in $PATH.
	$ torrent 'magnet:?xt=urn:btih:KRWPCX3SJUM4IMM4YF5RPHL6ANPYTQPU'
    ubuntu-14.04.2-desktop-amd64.iso [===================================================================>]  99% downloading (1.0 GB/1.0 GB)
    2015/04/01 02:08:20 main.go:137: downloaded ALL the torrents
    $ md5sum ubuntu-14.04.2-desktop-amd64.iso
    1b305d585b1918f297164add46784116  ubuntu-14.04.2-desktop-amd64.iso
    $ echo such amaze
    wow

### torrentfs

torrentfs mounts a FUSE filesystem at `-mountDir`. The contents are the torrents described by the torrent files and magnet links at `-metainfoDir`. Data for read requests is fetched only as required from the torrent network, and stored at `-downloadDir`.

    $ mkdir mnt torrents
    $ godo github.com/james-lawrence/torrent/cmd/torrentfs -mountDir=mnt -metainfoDir=torrents &
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

    $ godo github.com/james-lawrence/torrent/cmd/torrent-magnet < ubuntu-14.04.2-desktop-amd64.iso.torrent
	magnet:?xt=urn:btih:546cf15f724d19c4319cc17b179d7e035f89c1f4&dn=ubuntu-14.04.2-desktop-amd64.iso&tr=http%3A%2F%2Ftorrent.ubuntu.com%3A6969%2Fannounce&tr=http%3A%2F%2Fipv6.torrent.ubuntu.com%3A6969%2Fannounce
