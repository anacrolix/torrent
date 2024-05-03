# torrent

[![PkgGoDev](https://pkg.go.dev/badge/github.com/anacrolix/torrent)](https://pkg.go.dev/github.com/anacrolix/torrent)

This repository implements BitTorrent-related packages and command-line utilities in Go. The emphasis is on use as a library from other projects. It's been used 24/7 in production by downstream services since late 2014. The implementation was specifically created to explore Go's concurrency capabilities, and to include the ability to stream data directly from the BitTorrent network. To this end it [supports seeking, readaheads and other features](https://godoc.org/github.com/anacrolix/torrent#Reader) exposing torrents and their files with the various Go idiomatic `io` package interfaces. This is also demonstrated through [torrentfs](#torrentfs).

There is [support for protocol encryption, DHT, PEX, uTP, and various extensions](https://godoc.org/github.com/anacrolix/torrent). There are [several data storage backends provided](https://godoc.org/github.com/anacrolix/torrent/storage): blob, file, bolt, mmap, and sqlite, to name a few. You can [write your own](https://godoc.org/github.com/anacrolix/torrent/storage#ClientImpl) to store data for example on S3, or in a database. 

Some noteworthy package dependencies that can be used for other purposes include:

 * [go-libutp](https://github.com/anacrolix/go-libutp)
 * [dht](https://github.com/anacrolix/dht)
 * [bencode](https://godoc.org/github.com/anacrolix/torrent/bencode)
 * [tracker](https://godoc.org/github.com/anacrolix/torrent/tracker)

## Installation

Install the library package with `go get github.com/anacrolix/torrent`, or the provided cmds with `go install github.com/anacrolix/torrent/cmd/...@latest`.

## Library examples

There are some small [examples](https://godoc.org/github.com/anacrolix/torrent#pkg-examples) in the package documentation.

## Mentions

 * [@anacrolix](https://github.com/anacrolix) is interviewed about this repo in [Console 32](https://console.substack.com/p/console-32).

### Downstream projects

There are several web-frontends, sites, Android clients, storage backends and supporting services among the known public projects:

 * [cove](https://coveapp.info): Personal torrent browser with streaming, DHT search, video transcoding and casting.
 * [confluence](https://github.com/anacrolix/confluence): torrent client as a HTTP service <!-- Well of course I know him... He's me -->
 * [Gopeed](https://github.com/GopeedLab/gopeed): Gopeed (full name Go Speed), a high-speed downloader developed by Golang + Flutter, supports (HTTP, BitTorrent, Magnet) protocol, and supports all platforms. <!-- 7.7k stars --> 
 * [Erigon](https://github.com/ledgerwatch/erigon): an implementation of Ethereum (execution layer with embeddable consensus layer), on the efficiency frontier. <!-- 2.7k stars -->
 * [exatorrent](https://github.com/varbhat/exatorrent): Elegant self-hostable torrent client <!-- 1.5k stars -->
 * [bitmagnet](https://github.com/bitmagnet-io/bitmagnet): A self-hosted BitTorrent indexer, DHT crawler, content classifier and torrent search engine with web UI, GraphQL API and Servarr stack integration. <!-- 1.1k stars --> 
 * [TorrServer](https://github.com/YouROK/TorrServer): Torrent streaming server over http <!-- 984 stars -->
 * [distribyted](https://github.com/distribyted/distribyted): Distribyted is an alternative torrent client. It can expose torrent files as a standard FUSE, webDAV or HTTP endpoint and download them on demand, allowing random reads using a fixed amount of disk space. <!-- 982 stars -->
 * [Mangayomi](https://github.com/kodjodevf/mangayomi): Cross-platform app that allows users to read manga and stream anime from a variety of sources including BitTorrent. <!-- 940 stars -->
 * [Simple Torrent](https://github.com/boypt/simple-torrent): self-hosted HTTP remote torrent client <!-- 876 stars -->
 * [autobrr](https://github.com/autobrr/autobrr): autobrr redefines download automation for torrents and Usenet, drawing inspiration from tools like trackarr, autodl-irssi, and flexget. <!-- 855 stars -->
 * [mabel](https://github.com/smmr-software/mabel): Fancy BitTorrent client for the terminal <!-- 421 stars -->
 * [Toru](https://github.com/sweetbbak/toru): Stream anime from the the terminal! <!-- 216 stars -->
 * [webtor.io](https://webtor.io/): free cloud BitTorrent-client <!-- not exclusively anacrolix/torrent maybe? 40-200 stars? -->
 * [Android Torrent Client](https://gitlab.com/axet/android-torrent-client): Android torrent client <!-- 29 stars -->
 * [libtorrent](https://gitlab.com/axet/libtorrent): gomobile wrapper <!-- 15 stars -->
 * [Go-PeersToHTTP](https://github.com/WinPooh32/peerstohttp): Simple torrent proxy to http stream controlled over REST-like api <!-- 28 stars -->
 * [CortexFoundation/torrentfs](https://github.com/CortexFoundation/torrentfs): Independent HTTP service for file seeding and P2P file system of cortex full node <!-- 21 stars -->
 * [Torrent WebDAV Client](https://github.com/Jipok/torrent-webdav): Automatic torrent download, streaming, WebDAV server and client. <!-- 1 star, https://github.com/anacrolix/torrent/issues/917 --> 
 * [goTorrent](https://github.com/deranjer/goTorrent): torrenting server with a React web frontend <!-- 156 stars, inactive since 2020 -->
 * [Go Peerflix](https://github.com/Sioro-Neoku/go-peerflix): Start watching the movie while your torrent is still downloading! <!-- 449 stars, inactive since 2019 -->
 * [hTorrent](https://github.com/pojntfx/htorrent): HTTP to BitTorrent gateway with seeking support. <!-- 102 stars -->
 * [Remote-Torrent](https://github.com/BruceWangNo1/remote-torrent): Download Remotely and Retrieve Files Over HTTP <!-- 57 stars, inactive since 2019 -->
 * [Trickl](https://github.com/arranlomas/Trickl): torrent client for android <!-- 48 stars, inactive since 2018 -->
 * [ANT-Downloader](https://github.com/anatasluo/ant): ANT Downloader is a BitTorrent Client developed by golang, angular 7, and electron <!-- archived -->
 * [Elementum](http://elementum.surge.sh/) (up to version 0.0.71)

## Help

Communication about the project is primarily through [Discussions](https://github.com/anacrolix/torrent/discussions) and the [issue tracker](https://github.com/anacrolix/torrent/issues).

## Command packages

Here I'll describe what some of the packages in `./cmd` do. See [installation](#installation) to make them available.

### `torrent`

#### `torrent download`

Downloads torrents from the command-line.

    $ torrent download 'magnet:?xt=urn:btih:KRWPCX3SJUM4IMM4YF5RPHL6ANPYTQPU'
    ... lots of jibber jabber ...
    downloading "ubuntu-14.04.2-desktop-amd64.iso": 1.0 GB/1.0 GB, 1989/1992 pieces completed (1 partial)
    2015/04/01 02:08:20 main.go:137: downloaded ALL the torrents
    $ md5sum ubuntu-14.04.2-desktop-amd64.iso
    1b305d585b1918f297164add46784116  ubuntu-14.04.2-desktop-amd64.iso
    $ echo such amaze
    wow

#### `torrent metainfo magnet`

Creates a magnet link from a torrent file. Note the extracted trackers, display name, and info hash.

    $ torrent metainfo testdata/debian-10.8.0-amd64-netinst.iso.torrent magnet
    magnet:?xt=urn:btih:4090c3c2a394a49974dfbbf2ce7ad0db3cdeddd7&dn=debian-10.8.0-amd64-netinst.iso&tr=http%3A%2F%2Fbttracker.debian.org%3A6969%2Fannounce

See `torrent metainfo --help` for other metainfo related commands.

### `torrentfs`

torrentfs mounts a FUSE filesystem at `-mountDir`. The contents are the torrents described by the torrent files and magnet links at `-metainfoDir`. Data for read requests is fetched only as required from the torrent network, and stored at `-downloadDir`.

    $ mkdir mnt torrents
    $ torrentfs -mountDir=mnt -metainfoDir=torrents &
    $ cd torrents
    $ wget http://releases.ubuntu.com/14.04.2/ubuntu-14.04.2-desktop-amd64.iso.torrent
    $ cd ..
    $ ls mnt
    ubuntu-14.04.2-desktop-amd64.iso
    $ pv mnt/ubuntu-14.04.2-desktop-amd64.iso | md5sum
    996MB 0:04:40 [3.55MB/s] [========================================>] 100%
    1b305d585b1918f297164add46784116  -

