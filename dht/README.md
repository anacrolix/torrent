# dht

[![Go Reference](https://pkg.go.dev/badge/github.com/james-lawrence/torrent/dht.svg)](https://pkg.go.dev/github.com/james-lawrence/torrent/dht)

## Installation

Get the library package with `go get github.com/james-lawrence/torrent/dht`, or the provided cmds with `go install github.com/james-lawrence/torrent/dht/cmd/...@latest`.

## Commands

Here I'll describe what some of the provided commands in `./cmd` do.

### dht

Supports various commands operating on the DHT.

    % go run github.com/james-lawrence/torrent/dht/cmd/dht --help
    valid arguments at this point:
      --help|-h
      --network <string>
      --secure
      --bootstrap-addr <[]string>
      --query-resend-delay <time.Duration>
      derive-put-target
      put
      put-mutable-infohash
      get
      ping
      get-peers
      query
      ping-nodes

## Downstream projects

Projects that uses this repo in novel ways.

- [cove](https://coveapp.info): Torrent browser with streaming, DHT search, video transcoding and casting.
- [btlink](https://github.com/anacrolix/btlink): btlink supports DNS records stored on the DHT.
