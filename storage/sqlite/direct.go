package sqliteStorage

import (
	"errors"
	"fmt"
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
	NoCacheBlobs      bool
	BlobFlushInterval time.Duration
}

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewDirectStorage(opts NewDirectStorageOpts) (_ storage.ClientImplCloser, err error) {
	conn, err := newConn(opts.NewConnOpts)
	if err != nil {
		return
	}
	if opts.PageSize == 0 {
		// The largest size sqlite supports. I think we want this to be the smallest piece size we
		// can expect, which is probably 1<<17.
		opts.PageSize = 1 << 16
	}
	err = initDatabase(conn, opts.InitDbOpts)
	if err != nil {
		conn.Close()
		return
	}
	err = initConn(conn, opts.InitConnOpts)
	if err != nil {
		conn.Close()
		return
	}
	if opts.BlobFlushInterval == 0 && !opts.GcBlobs {
		// This is influenced by typical busy timeouts, of 5-10s. We want to give other connections
		// a few chances at getting a transaction through.
		opts.BlobFlushInterval = time.Second
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

func rowidForBlob(c conn, name string, length int64, create bool) (rowid int64, err error) {
	rowidOk := false
	err = sqlitex.Exec(c, "select rowid from blob where name=?", func(stmt *sqlite.Stmt) error {
		if rowidOk {
			panic("expected at most one row")
		}
		// TODO: How do we know if we got this wrong?
		rowid = stmt.ColumnInt64(0)
		rowidOk = true
		return nil
	}, name)
	if err != nil {
		return
	}
	if rowidOk {
		return
	}
	if !create {
		err = errors.New("no existing row")
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

func (p piece) doAtIoWithBlob(
	atIo func(*sqlite.Blob) func([]byte, int64) (int, error),
	b []byte,
	off int64,
	create bool,
) (n int, err error) {
	p.l.Lock()
	defer p.l.Unlock()
	if p.opts.NoCacheBlobs {
		defer p.forgetBlob()
	}
	blob, err := p.getBlob(create)
	if err != nil {
		err = fmt.Errorf("getting blob: %w", err)
		return
	}
	n, err = atIo(blob)(b, off)
	if err == nil {
		return
	}
	var se sqlite.Error
	if !errors.As(err, &se) {
		return
	}
	// "ABORT" occurs if the row the blob is on is modified elsewhere. "ERROR: invalid blob" occurs
	// if the blob has been closed. We don't forget blobs that are closed by our GC finalizers,
	// because they may be attached to names that have since moved on to another blob.
	if se.Code != sqlite.SQLITE_ABORT && !(p.opts.GcBlobs && se.Code == sqlite.SQLITE_ERROR && se.Msg == "invalid blob") {
		return
	}
	p.forgetBlob()
	// Try again, this time we're guaranteed to get a fresh blob, and so errors are no excuse. It
	// might be possible to skip to this version if we don't cache blobs.
	blob, err = p.getBlob(create)
	if err != nil {
		err = fmt.Errorf("getting blob: %w", err)
		return
	}
	return atIo(blob)(b, off)
}

func (p piece) ReadAt(b []byte, off int64) (n int, err error) {
	return p.doAtIoWithBlob(func(blob *sqlite.Blob) func([]byte, int64) (int, error) {
		return blob.ReadAt
	}, b, off, false)
}

func (p piece) WriteAt(b []byte, off int64) (n int, err error) {
	return p.doAtIoWithBlob(func(blob *sqlite.Blob) func([]byte, int64) (int, error) {
		return blob.WriteAt
	}, b, off, true)
}

func (p piece) MarkComplete() error {
	p.l.Lock()
	defer p.l.Unlock()
	err := sqlitex.Exec(p.conn, "update blob set verified=true where name=?", nil, p.name)
	if err != nil {
		return err
	}
	changes := p.conn.Changes()
	if changes != 1 {
		panic(changes)
	}
	return nil
}

func (p piece) forgetBlob() {
	blob, ok := p.blobs[p.name]
	if !ok {
		return
	}
	blob.Close()
	delete(p.blobs, p.name)
}

func (p piece) MarkNotComplete() error {
	p.l.Lock()
	defer p.l.Unlock()
	return sqlitex.Exec(p.conn, "update blob set verified=false where name=?", nil, p.name)
}

func (p piece) Completion() (ret storage.Completion) {
	p.l.Lock()
	defer p.l.Unlock()
	err := sqlitex.Exec(p.conn, "select verified from blob where name=?", func(stmt *sqlite.Stmt) error {
		ret.Complete = stmt.ColumnInt(0) != 0
		return nil
	}, p.name)
	ret.Ok = err == nil
	if err != nil {
		panic(err)
	}
	return
}

func (p piece) getBlob(create bool) (*sqlite.Blob, error) {
	blob, ok := p.blobs[p.name]
	if !ok {
		rowid, err := rowidForBlob(p.conn, p.name, p.length, create)
		if err != nil {
			return nil, fmt.Errorf("getting rowid for blob: %w", err)
		}
		blob, err = p.conn.OpenBlob("main", "blob", "data", rowid, true)
		if err != nil {
			panic(err)
		}
		if p.opts.GcBlobs {
			herp := new(byte)
			runtime.SetFinalizer(herp, func(*byte) {
				p.l.Lock()
				defer p.l.Unlock()
				// Note there's no guarantee that the finalizer fired while this blob is the same
				// one in the blob cache. It might be possible to rework this so that we check, or
				// strip finalizers as appropriate.
				blob.Close()
			})
		}
		p.blobs[p.name] = blob
	}
	return blob, nil
}
