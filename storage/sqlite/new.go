package sqliteStorage

import (
	"errors"
	"sync"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type NewDirectStorageOpts struct {
	NewPoolOpts
	ProvOpts func(*ProviderOpts)
}

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewDirectStorage(opts NewDirectStorageOpts) (_ storage.ClientImplCloser, err error) {
	conns, provOpts, err := NewPool(opts.NewPoolOpts)
	if err != nil {
		return
	}
	if f := opts.ProvOpts; f != nil {
		f(&provOpts)
	}
	provOpts.BatchWrites = false
	prov, err := NewProvider(conns, provOpts)
	if err != nil {
		conns.Close()
		return
	}
	return &client{
		prov:  prov,
		conn:  prov.pool.Get(nil),
		blobs: make(map[string]*sqlite.Blob),
	}, nil
}

type client struct {
	l     sync.Mutex
	prov  *provider
	conn  conn
	blobs map[string]*sqlite.Blob
}

func (c *client) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	return torrent{c}, nil
}

func (c *client) Close() error {
	for _, b := range c.blobs {
		b.Close()
	}
	c.prov.pool.Put(c.conn)
	return c.prov.Close()
}

type torrent struct {
	c *client
}

func rowidForBlob(c conn, name string, length int64) (rowid int64, err error) {
	err = sqlitex.Exec(c, "select rowid from blob where name=?", func(stmt *sqlite.Stmt) error {
		rowid = stmt.ColumnInt64(0)
		return nil
	}, name)
	if err != nil {
		return
	}
	if rowid != 0 {
		return
	}
	err = sqlitex.Exec(c, "insert into blob(name, data) values(?, zeroblob(?))", nil, name, length)
	if err != nil {
		return
	}
	rowid = c.LastInsertRowID()
	return
}

func (t torrent) Piece(p metainfo.Piece) storage.PieceImpl {
	t.c.l.Lock()
	defer t.c.l.Unlock()
	name := p.Hash().HexString()
	return piece{t.c.conn, &t.c.l, name, t.c.blobs, p.Length()}
}

func (t torrent) Close() error {
	return nil
}

type piece struct {
	conn   conn
	l      *sync.Mutex
	name   string
	blobs  map[string]*sqlite.Blob
	length int64
}

func (p2 piece) doAtIoWithBlob(
	atIo func(*sqlite.Blob) func([]byte, int64) (int, error),
	p []byte,
	off int64,
) (n int, err error) {
	p2.l.Lock()
	defer p2.l.Unlock()
	n, err = atIo(p2.getBlob())(p, off)
	var se sqlite.Error
	if !errors.As(err, &se) || se.Code != sqlite.SQLITE_ABORT {
		return
	}
	p2.blobWouldExpire()
	return atIo(p2.getBlob())(p, off)
}

func (p2 piece) ReadAt(p []byte, off int64) (n int, err error) {
	return p2.doAtIoWithBlob(func(blob *sqlite.Blob) func([]byte, int64) (int, error) {
		return blob.ReadAt
	}, p, off)
}

func (p2 piece) WriteAt(p []byte, off int64) (n int, err error) {
	return p2.doAtIoWithBlob(func(blob *sqlite.Blob) func([]byte, int64) (int, error) {
		return blob.WriteAt
	}, p, off)
}

func (p2 piece) MarkComplete() error {
	p2.l.Lock()
	defer p2.l.Unlock()
	err := sqlitex.Exec(p2.conn, "update blob set verified=true where name=?", nil, p2.name)
	if err != nil {
		return err
	}
	changes := p2.conn.Changes()
	if changes != 1 {
		panic(changes)
	}
	return nil
}

func (p2 piece) blobWouldExpire() {
	blob, ok := p2.blobs[p2.name]
	if !ok {
		return
	}
	blob.Close()
	delete(p2.blobs, p2.name)
}

func (p2 piece) MarkNotComplete() error {
	return sqlitex.Exec(p2.conn, "update blob set verified=false where name=?", nil, p2.name)
}

func (p2 piece) Completion() (ret storage.Completion) {
	p2.l.Lock()
	defer p2.l.Unlock()
	err := sqlitex.Exec(p2.conn, "select verified from blob where name=?", func(stmt *sqlite.Stmt) error {
		ret.Complete = stmt.ColumnInt(0) != 0
		return nil
	}, p2.name)
	ret.Ok = err == nil
	if err != nil {
		panic(err)
	}
	return
}

func (p2 piece) closeBlobIfExists() {
	if b, ok := p2.blobs[p2.name]; ok {
		b.Close()
		delete(p2.blobs, p2.name)
	}
}

func (p2 piece) getBlob() *sqlite.Blob {
	blob, ok := p2.blobs[p2.name]
	if !ok {
		rowid, err := rowidForBlob(p2.conn, p2.name, p2.length)
		if err != nil {
			panic(err)
		}
		blob, err = p2.conn.OpenBlob("main", "blob", "data", rowid, true)
		if err != nil {
			panic(err)
		}
		p2.blobs[p2.name] = blob
	}
	return blob
}
