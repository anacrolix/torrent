package torrent

import (
	"time"

	"github.com/anacrolix/missinggo"
	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/storage"
	"github.com/pkg/errors"
)

// Option for the torrent.
type Option func(*Metadata)

// OptionTrackers set the trackers for the torrent.
func OptionTrackers(trackers [][]string) Option {
	return func(t *Metadata) {
		t.Trackers = trackers
	}
}

// OptionNodes supplimental nodes to add to the dht of the client.
func OptionNodes(nodes ...string) Option {
	return func(t *Metadata) {
		t.DHTNodes = nodes
	}
}

// OptionDisplayName set the display name for the torrent.
func OptionDisplayName(dn string) Option {
	return func(t *Metadata) {
		t.DisplayName = dn
	}
}

// OptionInfo set the info bytes for the torrent.
func OptionInfo(i []byte) Option {
	return func(t *Metadata) {
		t.InfoBytes = i
	}
}

// OptionChunk sets the size of the chunks to use for outbound requests
func OptionChunk(s int) Option {
	return func(t *Metadata) {
		t.ChunkSize = s
	}
}

// OptionStorage set the storage implementation for the torrent.
func OptionStorage(s storage.ClientImpl) Option {
	return func(t *Metadata) {
		t.Storage = s
	}
}

// OptionWebseeds set the webseed hosts for the torrent.
func OptionWebseeds(seeds []string) Option {
	return func(t *Metadata) {
		t.Webseeds = seeds
	}
}

// Metadata specifies the metadata of a torrent for adding to a client.
// There are helpers for magnet URIs and torrent metainfo files.
type Metadata struct {
	// The tiered tracker URIs.
	Trackers  [][]string
	InfoHash  metainfo.Hash
	InfoBytes []byte
	// The name to use if the Name field from the Info isn't available.
	DisplayName string
	Webseeds    []string
	DHTNodes    []string
	// The chunk size to use for outbound requests. Defaults to 16KiB if not
	// set.
	ChunkSize int
	Storage   storage.ClientImpl
}

func (t Metadata) merge(options ...Option) Metadata {
	for _, opt := range options {
		opt(&t)
	}

	return t
}

// New create a torrent from the metainfo.MetaInfo and any additional options.
func New(info metainfo.Hash, options ...Option) (t Metadata, err error) {
	t = Metadata{
		InfoHash: info,
	}.merge(options...)

	return t, nil
}

// NewFromMetaInfoFile loads torrent metadata stored in a file.
func NewFromMetaInfoFile(path string, options ...Option) (t Metadata, err error) {
	var (
		mi *metainfo.MetaInfo
	)

	if mi, err = metainfo.LoadFromFile(path); err != nil {
		return t, err
	}

	return NewFromMetaInfo(mi, options...)
}

// NewFromFile convience method to create a torrent directly from a file.
func NewFromFile(path string, options ...Option) (t Metadata, err error) {
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

	if t, err = New(metainfo.HashBytes(encoded), OptionInfo(encoded), OptionDisplayName(info.Name)); err != nil {
		return t, errors.WithStack(err)
	}

	return t.merge(options...), nil
}

// NewFromMagnet creates a torrent from a magnet uri.
func NewFromMagnet(uri string) (t Metadata, err error) {
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
		OptionWebseeds(m.Params["ws"]),
	)
}

// NewFromInfo creates a torrent from metainfo.Info
func NewFromInfo(i metainfo.Info, options ...Option) (t Metadata, err error) {
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
func NewFromMetaInfo(mi *metainfo.MetaInfo, options ...Option) (t Metadata, err error) {
	var (
		info metainfo.Info
	)

	if info, err = mi.UnmarshalInfo(); err != nil {
		return t, err
	}

	options = append([]Option{
		OptionInfo(mi.InfoBytes),
		OptionDisplayName(info.Name),
		OptionTrackers(mi.UpvertedAnnounceList()),
		OptionWebseeds(mi.UrlList),
		OptionNodes(mi.NodeList()...),
	},
		options...,
	)

	return New(
		mi.HashInfoBytes(),
		options...,
	)
}

// Metainfo generate metainfo from the metadata.
func (t Metadata) Metainfo() metainfo.MetaInfo {
	return metainfo.MetaInfo{
		AnnounceList: make([][]string, 0),
		InfoBytes:    t.InfoBytes,
		CreationDate: time.Now().Unix(),
	}
}
