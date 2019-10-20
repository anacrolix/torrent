package torrent

import (
	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/pkg/errors"
)

// Option for the torrent.
type Option func(*Torrent)

// OptionTrackers set the trackers for the torrent.
func OptionTrackers(trackers [][]string) Option {
	return func(t *Torrent) {
		t.Trackers = trackers
	}
}

// OptionDisplayName set the display name for the torrent.
func OptionDisplayName(dn string) Option {
	return func(t *Torrent) {
		t.DisplayName = dn
	}
}

// OptionInfo set the info bytes for the torrent.
func OptionInfo(i []byte) Option {
	return func(t *Torrent) {
		t.InfoBytes = i
	}
}

// OptionChunk sets the size of the chunks to use for outbound requests
func OptionChunk(s int) Option {
	return func(t *Torrent) {
		t.ChunkSize = s
	}
}

// OptionStorage set the storage implementation for the torrent.
func OptionStorage(s storage.ClientImpl) Option {
	return func(t *Torrent) {
		t.Storage = s
	}
}

// Torrent specifies a new torrent for adding to a client.
// There are helpers for magnet URIs and torrent metainfo files.
type Torrent struct {
	// The tiered tracker URIs.
	Trackers  [][]string
	InfoHash  metainfo.Hash
	InfoBytes []byte
	// The name to use if the Name field from the Info isn't available.
	DisplayName string
	// The chunk size to use for outbound requests. Defaults to 16KiB if not
	// set.
	ChunkSize int
	Storage   storage.ClientImpl
}

func (t Torrent) merge(options ...Option) Torrent {
	for _, opt := range options {
		opt(&t)
	}

	return t
}

// New create a torrent from the metainfo.MetaInfo and any additional options.
func New(info metainfo.Hash, options ...Option) (t Torrent, err error) {
	t = Torrent{
		InfoHash: info,
	}.merge(options...)

	return t, nil
}

// NewFromFile convience method to create a torrent from a file.
func NewFromFile(path string, options ...Option) (t Torrent, err error) {
	var (
		encoded []byte
		info    = metainfo.Info{PieceLength: missinggo.MiB}
	)

	if err = info.BuildFromFilePath(path); err != nil {
		return t, errors.WithStack(err)
	}

	if encoded, err = bencode.Marshal(info); err != nil {
		return t, errors.WithStack(err)
	}

	return New(metainfo.HashBytes(encoded), options...)
}

// NewFromMagnet creates a torrent from a magnet uri.
func NewFromMagnet(uri string) (t Torrent, err error) {
	var (
		m metainfo.Magnet
	)

	if m, err = metainfo.ParseMagnetURI(uri); err != nil {
		return t, errors.WithStack(err)
	}

	return New(
		m.InfoHash,
		OptionDisplayName(m.DisplayName),
		OptionTrackers([][]string{m.Trackers}),
	)
}

// NewFromInfo creates a torrent from metainfo.Info
func NewFromInfo(i metainfo.Info, options ...Option) (t Torrent, err error) {
	var (
		encoded []byte
	)

	if encoded, err = bencode.Marshal(i); err != nil {
		return t, err
	}

	return New(
		metainfo.HashBytes(encoded),
		append(options, OptionInfo(encoded))...,
	)
}

// NewFromMetaInfo create a torrent from metainfo
func NewFromMetaInfo(mi *metainfo.MetaInfo, options ...Option) (t Torrent, err error) {
	var (
		info metainfo.Info
	)

	if info, err = mi.UnmarshalInfo(); err != nil {
		return t, err
	}

	trackers := mi.AnnounceList
	if trackers == nil {
		trackers = [][]string{{mi.Announce}}
	}

	options = append([]Option{
		OptionInfo(mi.InfoBytes),
		OptionDisplayName(info.Name),
		OptionTrackers(trackers),
	},
		options...,
	)

	return New(
		mi.HashInfoBytes(),
		options...,
	)
}
