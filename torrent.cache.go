package torrent

import (
	"sync"

	"github.com/james-lawrence/torrent/dht/int160"
)

func torrentCache(s MetadataStore, b BitmapStore) *memoryseeding {
	return &memoryseeding{
		_mu:           &sync.RWMutex{},
		MetadataStore: s,
		bm:            b,
		torrents:      make(map[int160.T]*torrent, 128),
	}
}

type memoryseeding struct {
	MetadataStore
	bm       BitmapStore
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

	// only record if the info is there.
	if c.haveInfo() {
		if c.chunks.Cardinality(c.chunks.missing) > 0 {
			if err := t.bm.Write(id, c.chunks.Clone(c.chunks.missing)); err != nil {
				return err
			}
		} else {
			if err := t.bm.Delete(id); err != nil {
				return err
			}
		}
	}

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

	// only record if the info is there.
	if len(md.InfoBytes) > 0 {
		if err := t.MetadataStore.Write(md); err != nil {
			return nil, err
		}
	}

	dlt := newTorrent(cl, md)
	t._mu.Lock()
	t.torrents[id] = dlt
	t._mu.Unlock()

	if len(md.InfoBytes) > 0 {
		if dlt.chunks.Cardinality(dlt.chunks.missing) > 0 {
			if err := t.bm.Write(id, dlt.chunks.Clone(dlt.chunks.missing)); err != nil {
				return nil, err
			}
		} else {
			if err := t.bm.Delete(id); err != nil {
				return nil, err
			}
		}
	}

	return dlt, nil
}

func (t *memoryseeding) Load(cl *Client, id int160.T) (_ *torrent, cached bool, _ error) {
	t._mu.RLock()
	x, ok := t.torrents[id]
	t._mu.RUnlock()

	if ok {
		return x, true, nil
	}

	t._mu.Lock()
	defer t._mu.Unlock()

	if x, ok := t.torrents[id]; ok {
		return x, true, nil
	}

	md, err := t.MetadataStore.Read(id)
	if err != nil {
		return nil, false, err
	}

	missing, err := t.bm.Read(id)
	if err != nil {
		return nil, false, err
	}

	dlt := newTorrent(cl, md)
	dlt.chunks.InitFromMissing(missing)

	t.torrents[id] = dlt

	return dlt, false, nil
}

func (t *memoryseeding) Metadata(id int160.T) (md Metadata, err error) {
	t._mu.Lock()
	defer t._mu.Unlock()

	if x, ok := t.torrents[id]; ok {
		return x.md, nil
	}

	return t.MetadataStore.Read(id)
}
