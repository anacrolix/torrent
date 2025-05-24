package torrent

import (
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/mse"
)

type MetadataStore interface {
	Read(id int160.T) (Metadata, error)
	Write(md Metadata) error
	Each() iter.Seq[int160.T]
}

func torrentCache(s MetadataStore) *memoryseeding {
	return &memoryseeding{
		_mu:           &sync.RWMutex{},
		MetadataStore: s,
		torrents:      make(map[int160.T]*torrent, 128),
	}
}

type memoryseeding struct {
	MetadataStore
	_mu      *sync.RWMutex
	torrents map[int160.T]*torrent
}

func (t *memoryseeding) Close() error {
	t._mu.Lock()
	defer t._mu.Unlock()

	for _, c := range t.torrents {
		if err := c.close(); err != nil {
			return err
		}
	}

	return nil
}

// clear torrent from memory
func (t *memoryseeding) Drop(id int160.T) error {
	t._mu.RLock()
	c, ok := t.torrents[id]
	t._mu.RUnlock()

	if !ok {
		return nil
	}

	t._mu.Lock()
	delete(t.torrents, id)
	t._mu.Unlock()

	return c.close()
}

func (t *memoryseeding) Insert(cl *Client, md Metadata) (*torrent, error) {
	id := int160.FromBytes(md.ID.Bytes())
	t._mu.RLock()
	x, ok := t.torrents[id]
	t._mu.RUnlock()

	if ok {
		return x, nil
	}

	if err := t.MetadataStore.Write(md); err != nil {
		return nil, err
	}

	dlt := newTorrent(cl, md)
	t._mu.Lock()
	t.torrents[id] = dlt
	t._mu.Unlock()

	return dlt, nil
}

func (t *memoryseeding) Load(cl *Client, id int160.T) (_ *torrent, cached bool, _ error) {
	t._mu.RLock()
	x, ok := t.torrents[id]
	t._mu.RUnlock()

	if ok {
		return x, true, nil
	}

	md, err := t.MetadataStore.Read(id)
	if err != nil {
		return nil, false, err
	}

	dlt := newTorrent(cl, md)

	t._mu.Lock()
	defer t._mu.Unlock()
	t.torrents[id] = dlt

	// TODO: we'll want an as needed verification
	return dlt, false, dlt.Tune(TuneVerifyAsync)
}

func (t *memoryseeding) Metadata(id int160.T) (md Metadata, err error) {
	t._mu.Lock()
	defer t._mu.Unlock()

	if x, ok := t.torrents[id]; ok {
		return x.md, nil
	}

	return t.MetadataStore.Read(id)
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
	return filepath.Join(t.root, fmt.Sprintf("%s.torrent", id.String()))
}

func (t metadatafilestore) Read(id int160.T) (Metadata, error) {
	return NewFromMetaInfoFile(t.path(id))
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
