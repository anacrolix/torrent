package sqliteStorage

import (
	"errors"
	"runtime"
	"sync"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type NewDirectStorageOpts struct {
	NewConnOpts
	InitDbOpts
	InitConnOpts
	GcBlobs           bool
	CacheBlobs        bool
	BlobFlushInterval time.Duration
}

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewDirectStorage(opts NewDirectStorageOpts) (_ storage.ClientImplCloser, err error) {
	conn, err := newConn(opts.NewConnOpts)
	if err != nil {
		return
	}
	err = initConn(conn, opts.InitConnOpts)
	if err != nil {
		conn.Close()
		return
	}
	err = initDatabase(conn, opts.InitDbOpts)
	if err != nil {
		return
	}
	cl := &client{
		conn:  conn,
		blobs: make(map[string]*sqlite.Blob),
		opts:  opts,
	}
	if opts.BlobFlushInterval != 0 {
		cl.blobFlusher = time.AfterFunc(opts.BlobFlushInterval, cl.blobFlusherFunc)
	}
	return cl, nil
}

type client struct {
	l           sync.Mutex
	conn        conn
	blobs       map[string]*sqlite.Blob
	blobFlusher *time.Timer
	opts        NewDirectStorageOpts
	closed      bool
}

func (c *client) blobFlusherFunc() {
	c.l.Lock()
	defer c.l.Unlock()
	c.flushBlobs()
	if !c.closed {
		c.blobFlusher.Reset(c.opts.BlobFlushInterval)
	}
}

func (c *client) flushBlobs() {
	for key, b := range c.blobs {
		// Need the lock to prevent racing with the GC finalizers.
		b.Close()
		delete(c.blobs, key)
	}
}

func (c *client) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	return torrent{c}, nil
}

func (c *client) Close() error {
	c.l.Lock()
	defer c.l.Unlock()
	c.flushBlobs()
	c.closed = true
	if c.opts.BlobFlushInterval != 0 {
		c.blobFlusher.Stop()
	}
	return c.conn.Close()
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
	return piece{
		name,
		p.Length(),
		t.c,
	}
}

func (t torrent) Close() error {
	return nil
}

type piece struct {
	name   string
	length int64
	*client
}

func (p2 piece) doAtIoWithBlob(
	atIo func(*sqlite.Blob) func([]byte, int64) (int, error),
	p []byte,
	off int64,
) (n int, err error) {
	p2.l.Lock()
	defer p2.l.Unlock()
	if !p2.opts.CacheBlobs {
		defer p2.forgetBlob()
	}
	n, err = atIo(p2.getBlob())(p, off)
	if err == nil {
		return
	}
	var se sqlite.Error
	if !errors.As(err, &se) {
		return
	}
	if se.Code != sqlite.SQLITE_ABORT && !(p2.opts.GcBlobs && se.Code == sqlite.SQLITE_ERROR && se.Msg == "invalid blob") {
		return
	}
	p2.forgetBlob()
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

func (p2 piece) forgetBlob() {
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
		if p2.opts.GcBlobs {
			herp := new(byte)
			runtime.SetFinalizer(herp, func(*byte) {
				p2.l.Lock()
				defer p2.l.Unlock()
				blob.Close()
			})
		}
		p2.blobs[p2.name] = blob
	}
	return blob
}
