package main

import (
	"github.com/anacrolix/bargle/v2"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent/types/infohash"
)

// Work to do once the entire command line has parsed successfully.
type action func() error

type subcommand = bargle.Subcommand[action]

// Shorthand for bargle.WithDesc, which gets a lot of use here.
func desc(s string, arg bargle.Arg) bargle.Arg {
	return bargle.WithDesc(s, arg)
}

// Byte counts are parsed from strings like "16MB", so they read better in help as "bytes" than as
// the Go type name.
func bytesUnmarshaler(target *tagflag.Bytes) bargle.Unmarshaler {
	return bargle.WithArgTypes(bargle.TextUnmarshaler(target), "bytes")
}

func infoHashUnmarshaler(target *infohash.T) bargle.Unmarshaler {
	return bargle.TextUnmarshaler(target)
}
