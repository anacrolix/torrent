package torrent

import (
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/mse"
)

type MetadataStore interface {
	Read(id int160.T) (Metadata, error)
	Write(md Metadata) error
	Each() iter.Seq[int160.T]
}

func NewMetadataCache(root string) metadatafilestore {
	if err := os.MkdirAll(root, 0700); err != nil {
		log.Println("unable to ensure metadata cache root directory", err)
	}

	return metadatafilestore{
		root: root,
	}
}

type metadatafilestore struct {
	root string
}

func (t metadatafilestore) path(id int160.T) string {
	return filepath.Join(t.root, fmt.Sprintf("%s.torrent", id))
}

func (t metadatafilestore) Read(id int160.T) (Metadata, error) {
	p := t.path(id)
	if md, err := NewFromMetaInfoFile(p); err == nil {
		return md, nil
	}
	return NewFromInfoFile(p)
}

func (t metadatafilestore) Write(md Metadata) error {
	encoded, err := metainfo.Encode(md.Metainfo())
	if err != nil {
		return err
	}

	return os.WriteFile(t.path(int160.FromByteArray(md.ID)), encoded, 0600)
}

func (t metadatafilestore) Each() iter.Seq[int160.T] {
	return mse.DirectoryNameSecrets(t.root)
}
