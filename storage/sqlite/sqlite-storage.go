package sqliteStorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"runtime"
	"sync"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/v2/resource"
	"github.com/anacrolix/torrent/storage"
)

type conn = *sqlite.Conn

func initConn(conn conn, wal bool) error {
	err := sqlitex.ExecTransient(conn, `pragma synchronous=off`, nil)
	if err != nil {
		return err
	}
	if !wal {
		err = sqlitex.ExecTransient(conn, `pragma journal_mode=off`, nil)
		if err != nil {
			return err
		}
	}
	err = sqlitex.ExecTransient(conn, `pragma mmap_size=1000000000000`, nil)
	if err != nil {
		return err
	}
	return nil
}

func initSchema(conn conn) error {
	return sqlitex.ExecScript(conn, `
pragma auto_vacuum=incremental;

create table if not exists blob(
	name text,
	last_used timestamp default (datetime('now')),
	data blob,
	primary key (name)
);

create table if not exists setting(
	name primary key on conflict replace,
	value
);

create view if not exists deletable_blob as
with recursive excess(
	usage_with,
	last_used,
	blob_rowid,
	data_length
) as (
	select * from (select (select sum(length(cast(data as blob))) from blob) as usage_with, last_used, rowid, length(cast(data as blob)) from blob order by last_used, rowid limit 1)
		where usage_with >= (select value from setting where name='capacity')
	union all
	select usage_with-data_length, blob.last_used, blob.rowid, length(cast(data as blob)) from excess join blob
		on blob.rowid=(select rowid from blob where (last_used, rowid) > (excess.last_used, blob_rowid))
	where usage_with >= (select value from setting where name='capacity')
) select * from excess;

CREATE TRIGGER if not exists trim_blobs_to_capacity_after_update after update on blob begin 
	delete from blob where rowid in (select blob_rowid from deletable_blob);
end;
CREATE TRIGGER if not exists trim_blobs_to_capacity_after_insert after insert on blob begin
	delete from blob where rowid in (select blob_rowid from deletable_blob);
end;
`)
}

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewPiecesStorage(opts NewPoolOpts) (_ storage.ClientImplCloser, err error) {
	conns, provOpts, err := NewPool(opts)
	if err != nil {
		return
	}
	prov, err := NewProvider(conns, provOpts)
	if err != nil {
		conns.Close()
		return
	}
	store := storage.NewResourcePieces(prov)
	return struct {
		storage.ClientImpl
		io.Closer
	}{
		store,
		prov,
	}, nil
}

type NewPoolOpts struct {
	Path     string
	Memory   bool
	NumConns int
	// Forces WAL, disables shared caching.
	ConcurrentBlobReads bool
}

// There's some overlap here with NewPoolOpts, and I haven't decided what needs to be done. For now,
// the fact that the pool opts are a superset, means our helper NewPiecesStorage can just take the
// top-level option type.
type ProviderOpts struct {
	NumConns int
	// Concurrent blob reads require WAL.
	ConcurrentBlobRead bool
	BatchWrites        bool
}

func NewPool(opts NewPoolOpts) (_ ConnPool, _ ProviderOpts, err error) {
	if opts.NumConns == 0 {
		opts.NumConns = runtime.NumCPU()
	}
	if opts.Memory {
		opts.Path = ":memory:"
	}
	values := make(url.Values)
	if !opts.ConcurrentBlobReads {
		values.Add("cache", "shared")
	}
	path := fmt.Sprintf("file:%s?%s", opts.Path, values.Encode())
	conns, err := func() (ConnPool, error) {
		switch opts.NumConns {
		case 1:
			conn, err := sqlite.OpenConn(path, 0)
			return &poolFromConn{conn: conn}, err
		default:
			return sqlitex.Open(path, 0, opts.NumConns)
		}
	}()
	if err != nil {
		return
	}
	return conns, ProviderOpts{
		NumConns:           opts.NumConns,
		ConcurrentBlobRead: opts.ConcurrentBlobReads,
		BatchWrites:        true,
	}, nil
}

// Emulates a ConnPool from a single Conn. Might be faster than using a sqlitex.Pool.
type poolFromConn struct {
	mu   sync.Mutex
	conn conn
}

func (me *poolFromConn) Get(ctx context.Context) conn {
	me.mu.Lock()
	return me.conn
}

func (me *poolFromConn) Put(conn conn) {
	if conn != me.conn {
		panic("expected to same conn")
	}
	me.mu.Unlock()
}

func (me *poolFromConn) Close() error {
	return me.conn.Close()
}

// Needs the ConnPool size so it can initialize all the connections with pragmas. Takes ownership of
// the ConnPool (since it has to initialize all the connections anyway).
func NewProvider(pool ConnPool, opts ProviderOpts) (_ *provider, err error) {
	_, err = initPoolConns(context.TODO(), pool, opts.NumConns, true)
	if err != nil {
		return
	}
	conn := pool.Get(context.TODO())
	defer pool.Put(conn)
	err = initSchema(conn)
	if err != nil {
		return
	}
	writes := make(chan writeRequest)
	prov := &provider{pool: pool, writes: writes, opts: opts}
	go prov.writer(writes)
	return prov, nil
}

func initPoolConns(ctx context.Context, pool ConnPool, numConn int, wal bool) (numInited int, err error) {
	var conns []conn
	defer func() {
		for _, c := range conns {
			pool.Put(c)
		}
	}()
	for range iter.N(numConn) {
		conn := pool.Get(ctx)
		if conn == nil {
			break
		}
		conns = append(conns, conn)
		err = initConn(conn, wal)
		if err != nil {
			err = fmt.Errorf("initing conn %v: %w", len(conns), err)
			return
		}
		numInited++
	}
	return
}

type ConnPool interface {
	Get(context.Context) conn
	Put(conn)
	Close() error
}

type provider struct {
	pool   ConnPool
	writes chan<- writeRequest
	opts   ProviderOpts
}

func (me *provider) Close() error {
	close(me.writes)
	return me.pool.Close()
}

type writeRequest struct {
	query func(*sqlite.Conn)
	done  chan<- struct{}
}

func (me *provider) writer(writes <-chan writeRequest) {
	for {
		first, ok := <-writes
		if !ok {
			return
		}
		buf := []writeRequest{first}
	buffer:
		for {
			select {
			case wr, ok := <-writes:
				if !ok {
					break buffer
				}
				buf = append(buf, wr)
			default:
				break buffer
			}
		}
		var cantFail error
		func() {
			conn := me.pool.Get(context.TODO())
			defer me.pool.Put(conn)
			defer sqlitex.Save(conn)(&cantFail)
			for _, wr := range buf {
				wr.query(conn)
			}
		}()
		if cantFail != nil {
			panic(cantFail)
		}
		for _, wr := range buf {
			close(wr.done)
		}
		log.Printf("batched %v write queries", len(buf))
	}
}

func (p *provider) NewInstance(s string) (resource.Instance, error) {
	return instance{s, p}, nil
}

type instance struct {
	location string
	p        *provider
}

func (i instance) withConn(with func(conn conn), write bool) {
	if write && i.p.opts.BatchWrites {
		done := make(chan struct{})
		i.p.writes <- writeRequest{
			query: with,
			done:  done,
		}
		<-done
	} else {
		conn := i.p.pool.Get(context.TODO())
		defer i.p.pool.Put(conn)
		with(conn)
	}
}

func (i instance) getConn() *sqlite.Conn {
	return i.p.pool.Get(context.TODO())
}

func (i instance) putConn(conn *sqlite.Conn) {
	i.p.pool.Put(conn)
}

func (i instance) Readdirnames() (names []string, err error) {
	prefix := i.location + "/"
	i.withConn(func(conn conn) {
		err = sqlitex.Exec(conn, "select name from blob where name like ?", func(stmt *sqlite.Stmt) error {
			names = append(names, stmt.ColumnText(0)[len(prefix):])
			return nil
		}, prefix+"%")
	}, false)
	//log.Printf("readdir %q gave %q", i.location, names)
	return
}

func (i instance) getBlobRowid(conn conn) (rowid int64, err error) {
	rows := 0
	err = sqlitex.Exec(conn, "select rowid from blob where name=?", func(stmt *sqlite.Stmt) error {
		rowid = stmt.ColumnInt64(0)
		rows++
		return nil
	}, i.location)
	if err != nil {
		return
	}
	if rows == 1 {
		return
	}
	if rows == 0 {
		err = errors.New("blob not found")
		return
	}
	panic(rows)
}

type connBlob struct {
	*sqlite.Blob
	onClose func()
}

func (me connBlob) Close() error {
	err := me.Blob.Close()
	me.onClose()
	return err
}

func (i instance) Get() (ret io.ReadCloser, err error) {
	conn := i.getConn()
	blob, err := i.openBlob(conn, false, true)
	if err != nil {
		i.putConn(conn)
		return
	}
	var once sync.Once
	return connBlob{blob, func() {
		once.Do(func() { i.putConn(conn) })
	}}, nil
}

func (i instance) openBlob(conn conn, write, updateAccess bool) (*sqlite.Blob, error) {
	rowid, err := i.getBlobRowid(conn)
	if err != nil {
		return nil, err
	}
	// This seems to cause locking issues with in-memory databases. Is it something to do with not
	// having WAL?
	if updateAccess {
		err = sqlitex.Exec(conn, "update blob set last_used=datetime('now') where rowid=?", nil, rowid)
		if err != nil {
			err = fmt.Errorf("updating last_used: %w", err)
			return nil, err
		}
		if conn.Changes() != 1 {
			panic(conn.Changes())
		}
	}
	return conn.OpenBlob("main", "blob", "data", rowid, write)
}

func (i instance) Put(reader io.Reader) (err error) {
	var buf bytes.Buffer
	_, err = io.Copy(&buf, reader)
	if err != nil {
		return err
	}
	i.withConn(func(conn conn) {
		for range iter.N(10) {
			err = sqlitex.Exec(conn,
				"insert or replace into blob(name, data) values(?, cast(? as blob))",
				nil,
				i.location, buf.Bytes())
			if err, ok := err.(sqlite.Error); ok && err.Code == sqlite.SQLITE_BUSY {
				log.Print("sqlite busy")
				time.Sleep(time.Second)
				continue
			}
			break
		}
	}, true)
	return
}

type fileInfo struct {
	size int64
}

func (f fileInfo) Name() string {
	panic("implement me")
}

func (f fileInfo) Size() int64 {
	return f.size
}

func (f fileInfo) Mode() os.FileMode {
	panic("implement me")
}

func (f fileInfo) ModTime() time.Time {
	panic("implement me")
}

func (f fileInfo) IsDir() bool {
	panic("implement me")
}

func (f fileInfo) Sys() interface{} {
	panic("implement me")
}

func (i instance) Stat() (ret os.FileInfo, err error) {
	i.withConn(func(conn conn) {
		var blob *sqlite.Blob
		blob, err = i.openBlob(conn, false, false)
		if err != nil {
			return
		}
		defer blob.Close()
		ret = fileInfo{blob.Size()}
	}, false)
	return
}

func (i instance) ReadAt(p []byte, off int64) (n int, err error) {
	i.withConn(func(conn conn) {
		if false {
			var blob *sqlite.Blob
			blob, err = i.openBlob(conn, false, true)
			if err != nil {
				return
			}
			defer blob.Close()
			if off >= blob.Size() {
				err = io.EOF
				return
			}
			if off+int64(len(p)) > blob.Size() {
				p = p[:blob.Size()-off]
			}
			n, err = blob.ReadAt(p, off)
		} else {
			gotRow := false
			err = sqlitex.Exec(
				conn,
				"select substr(cast(data as blob), ?, ?) from blob where name=?",
				func(stmt *sqlite.Stmt) error {
					if gotRow {
						panic("found multiple matching blobs")
					} else {
						gotRow = true
					}
					n = stmt.ColumnBytes(0, p)
					return nil
				},
				off+1, len(p), i.location,
			)
			if err != nil {
				return
			}
			if !gotRow {
				err = errors.New("blob not found")
				return
			}
			if n < len(p) {
				err = io.EOF
			}
		}
	}, false)
	return
}

func (i instance) WriteAt(bytes []byte, i2 int64) (int, error) {
	panic("implement me")
}

func (i instance) Delete() (err error) {
	i.withConn(func(conn conn) {
		err = sqlitex.Exec(conn, "delete from blob where name=?", nil, i.location)
	}, true)
	return
}
