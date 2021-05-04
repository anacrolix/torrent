package sqliteStorage

import (
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
		prov: prov,
		conn: prov.pool.Get(nil),
	}, nil
}

type client struct {
	l    sync.Mutex
	prov *provider
	conn conn
	blob *sqlite.Blob
}

func (c *client) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	return torrent{c}, nil
}

func (c *client) Close() error {
	if c.blob != nil {
		c.blob.Close()
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
	return piece{t.c.conn, name, &t.c.l, p.Length(), &t.c.blob}
}

func (t torrent) Close() error {
	return nil
}

type piece struct {
	conn   conn
	name   string
	l      *sync.Mutex
	length int64
	blob   **sqlite.Blob
}

func (p2 piece) getBlob() *sqlite.Blob {
	if *p2.blob != nil {
		err := (*p2.blob).Close()
		if err != nil {
			panic(err)
		}
		*p2.blob = nil
	}
	rowid, err := rowidForBlob(p2.conn, p2.name, p2.length)
	if err != nil {
		panic(err)
	}
	*p2.blob, err = p2.conn.OpenBlob("main", "blob", "data", rowid, true)
	if err != nil {
		panic(err)
	}
	return *p2.blob
}

func (p2 piece) ReadAt(p []byte, off int64) (n int, err error) {
	p2.l.Lock()
	defer p2.l.Unlock()
	blob := p2.getBlob()
	return blob.ReadAt(p, off)
}

func (p2 piece) WriteAt(p []byte, off int64) (n int, err error) {
	p2.l.Lock()
	defer p2.l.Unlock()
	return p2.getBlob().WriteAt(p, off)
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

func (p2 piece) MarkNotComplete() error {
	panic("implement me")
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
