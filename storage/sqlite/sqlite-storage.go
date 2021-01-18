package sqliteStorage

import (
	"bytes"
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
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
	// Recursive triggers are required because we need to trim the blob_meta size after trimming to
	// capacity. Hopefully we don't hit the recursion limit, and if we do, there's an error thrown.
	err := sqlitex.ExecTransient(conn, "pragma recursive_triggers=on", nil)
	if err != nil {
		return err
	}
	err = sqlitex.ExecTransient(conn, `pragma synchronous=off`, nil)
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

func initSchema(conn conn, pageSize int, triggers bool) error {
	if pageSize != 0 {
		err := sqlitex.ExecScript(conn, fmt.Sprintf("pragma page_size=%d", pageSize))
		if err != nil {
			return err
		}
	}
	err := sqlitex.ExecScript(conn, `
		-- We have to opt into this before creating any tables, or before a vacuum to enable it. It means we
		-- can trim the database file size with partial vacuums without having to do a full vacuum, which 
		-- locks everything.
		pragma auto_vacuum=incremental;
		
		create table if not exists blob (
			name text,
			last_used timestamp default (datetime('now')),
			data blob,
			primary key (name)
		);
		
		create table if not exists blob_meta (
			key text primary key,
			value
		);
		
		-- While sqlite *seems* to be faster to get sum(length(data)) instead of 
		-- sum(length(cast(data as blob))), it may still require a large table scan at start-up or with a 
		-- cold-cache. With this we can be assured that it doesn't.
		insert or ignore into blob_meta values ('size', 0);
		
		create table if not exists setting (
			name primary key on conflict replace,
			value
		);
	
		create view if not exists deletable_blob as
		with recursive excess (
			usage_with,
			last_used,
			blob_rowid,
			data_length
		) as (
			select * 
			from (
				select 
					(select value from blob_meta where key='size') as usage_with,
					last_used,
					rowid,
					length(cast(data as blob))
				from blob order by last_used, rowid limit 1
			)
			where usage_with > (select value from setting where name='capacity')
			union all
			select 
				usage_with-data_length as new_usage_with,
				blob.last_used,
				blob.rowid,
				length(cast(data as blob))
			from excess join blob
			on blob.rowid=(select rowid from blob where (last_used, rowid) > (excess.last_used, blob_rowid))
			where new_usage_with > (select value from setting where name='capacity')
		)
		select * from excess;
	`)
	if err != nil {
		return err
	}
	if triggers {
		err := sqlitex.ExecScript(conn, `
			create trigger if not exists after_insert_blob
			after insert on blob
			begin
				update blob_meta set value=value+length(cast(new.data as blob)) where key='size';
				delete from blob where rowid in (select blob_rowid from deletable_blob);
			end;
			
			create trigger if not exists after_update_blob
			after update of data on blob
			begin
				update blob_meta set value=value+length(cast(new.data as blob))-length(cast(old.data as blob)) where key='size';
				delete from blob where rowid in (select blob_rowid from deletable_blob);
			end;
			
			create trigger if not exists after_delete_blob
			after delete on blob
			begin
				update blob_meta set value=value-length(cast(old.data as blob)) where key='size';
			end;
		`)
		if err != nil {
			return err
		}
	}
	return nil
}

type NewPiecesStorageOpts struct {
	NewPoolOpts
	ProvOpts func(*ProviderOpts)
	storage.ResourcePiecesOpts
}

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewPiecesStorage(opts NewPiecesStorageOpts) (_ storage.ClientImplCloser, err error) {
	conns, provOpts, err := NewPool(opts.NewPoolOpts)
	if err != nil {
		return
	}
	if f := opts.ProvOpts; f != nil {
		f(&provOpts)
	}
	prov, err := NewProvider(conns, provOpts)
	if err != nil {
		conns.Close()
		return
	}
	store := storage.NewResourcePiecesOpts(prov, opts.ResourcePiecesOpts)
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
	NoConcurrentBlobReads bool
	DontInitSchema        bool
	PageSize              int
	// If non-zero, overrides the existing setting.
	Capacity int64
}

// There's some overlap here with NewPoolOpts, and I haven't decided what needs to be done. For now,
// the fact that the pool opts are a superset, means our helper NewPiecesStorage can just take the
// top-level option type.
type ProviderOpts struct {
	NumConns int
	// Concurrent blob reads require WAL.
	NoConcurrentBlobReads bool
	BatchWrites           bool
}

// Remove any capacity limits.
func UnlimitCapacity(conn conn) error {
	return sqlitex.Exec(conn, "delete from setting where key='capacity'", nil)
}

// Set the capacity limit to exactly this value.
func SetCapacity(conn conn, cap int64) error {
	return sqlitex.Exec(conn, "insert into setting values ('capacity', ?)", nil, cap)
}

func NewPool(opts NewPoolOpts) (_ ConnPool, _ ProviderOpts, err error) {
	if opts.NumConns == 0 {
		opts.NumConns = runtime.NumCPU()
	}
	if opts.Memory {
		opts.Path = ":memory:"
	}
	values := make(url.Values)
	if opts.NoConcurrentBlobReads || opts.Memory {
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
	defer func() {
		if err != nil {
			conns.Close()
		}
	}()
	conn := conns.Get(context.TODO())
	defer conns.Put(conn)
	if !opts.DontInitSchema {
		if opts.PageSize == 0 {
			opts.PageSize = 1 << 14
		}
		err = initSchema(conn, opts.PageSize, true)
		if err != nil {
			return
		}
	}
	if opts.Capacity != 0 {
		err = SetCapacity(conn, opts.Capacity)
		if err != nil {
			return
		}
	}
	return conns, ProviderOpts{
		NumConns:              opts.NumConns,
		NoConcurrentBlobReads: opts.NoConcurrentBlobReads || opts.Memory,
		BatchWrites:           true,
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
	_, err = initPoolConns(context.TODO(), pool, opts.NumConns, !opts.NoConcurrentBlobReads)
	if err != nil {
		err = fmt.Errorf("initing pool conns: %w", err)
		return
	}
	prov := &provider{pool: pool, opts: opts}
	if opts.BatchWrites {
		if opts.NumConns < 2 {
			err = errors.New("batch writes requires more than 1 conn")
			return
		}
		writes := make(chan writeRequest)
		prov.writes = writes
		// This is retained for backwards compatibility. It may not be necessary.
		runtime.SetFinalizer(prov, func(p *provider) {
			p.Close()
		})
		go providerWriter(writes, prov.pool)
	}
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
	pool     ConnPool
	writes   chan<- writeRequest
	opts     ProviderOpts
	closed   sync.Once
	closeErr error
}

var _ storage.ConsecutiveChunkWriter = (*provider)(nil)

func (p *provider) WriteConsecutiveChunks(prefix string, w io.Writer) (written int64, err error) {
	err = p.withConn(func(_ context.Context, conn conn) error {
		err = io.EOF
		err = sqlitex.Exec(conn, `
				select
					cast(data as blob),
					cast(substr(name, ?+1) as integer) as offset
				from blob
				where name like ?||'%'
				order by offset`,
			func(stmt *sqlite.Stmt) error {
				offset := stmt.ColumnInt64(1)
				if offset != written {
					return fmt.Errorf("got chunk at offset %v, expected offset %v", offset, written)
				}
				r := stmt.ColumnReader(0)
				w1, err := io.Copy(w, r)
				written += w1
				return err
			},
			len(prefix),
			prefix,
		)
		return err
	}, false, 0)
	return
}

func (me *provider) Close() error {
	me.closed.Do(func() {
		if me.writes != nil {
			close(me.writes)
		}
		me.closeErr = me.pool.Close()
	})
	return me.closeErr
}

type writeRequest struct {
	query  withConn
	done   chan<- error
	labels pprof.LabelSet
}

var expvars = expvar.NewMap("sqliteStorage")

func runQueryWithLabels(query withConn, labels pprof.LabelSet, conn conn) (err error) {
	pprof.Do(context.Background(), labels, func(ctx context.Context) {
		// We pass in the context in the hope that the CPU profiler might incorporate sqlite
		// activity the action that triggered it. It doesn't seem that way, since those calls don't
		// take a context.Context themselves. It may come in useful in the goroutine profiles
		// though, and doesn't hurt to expose it here for other purposes should things change.
		err = query(ctx, conn)
	})
	return
}

// Intentionally avoids holding a reference to *provider to allow it to use a finalizer, and to have
// stronger typing on the writes channel.
func providerWriter(writes <-chan writeRequest, pool ConnPool) {
	conn := pool.Get(context.TODO())
	if conn == nil {
		return
	}
	defer pool.Put(conn)
	for {
		first, ok := <-writes
		if !ok {
			return
		}
		var buf []func()
		var cantFail error
		func() {
			defer sqlitex.Save(conn)(&cantFail)
			firstErr := runQueryWithLabels(first.query, first.labels, conn)
			buf = append(buf, func() { first.done <- firstErr })
			for {
				select {
				case wr, ok := <-writes:
					if ok {
						err := runQueryWithLabels(wr.query, wr.labels, conn)
						buf = append(buf, func() { wr.done <- err })
						continue
					}
				default:
				}
				break
			}
		}()
		// Not sure what to do if this failed.
		if cantFail != nil {
			expvars.Add("batchTransactionErrors", 1)
		}
		// Signal done after we know the transaction succeeded.
		for _, done := range buf {
			done()
		}
		expvars.Add("batchTransactions", 1)
		expvars.Add("batchedQueries", int64(len(buf)))
		//log.Printf("batched %v write queries", len(buf))
	}
}

func (p *provider) NewInstance(s string) (resource.Instance, error) {
	return instance{s, p}, nil
}

type instance struct {
	location string
	p        *provider
}

func getLabels(skip int) pprof.LabelSet {
	return pprof.Labels("sqlite-storage-action", func() string {
		var pcs [8]uintptr
		runtime.Callers(skip+3, pcs[:])
		fs := runtime.CallersFrames(pcs[:])
		f, _ := fs.Next()
		funcName := f.Func.Name()
		funcName = funcName[strings.LastIndexByte(funcName, '.')+1:]
		//log.Printf("func name: %q", funcName)
		return funcName
	}())
}

func (p *provider) withConn(with withConn, write bool, skip int) error {
	if write && p.opts.BatchWrites {
		done := make(chan error)
		p.writes <- writeRequest{
			query:  with,
			done:   done,
			labels: getLabels(skip + 1),
		}
		return <-done
	} else {
		conn := p.pool.Get(context.TODO())
		if conn == nil {
			return errors.New("couldn't get pool conn")
		}
		defer p.pool.Put(conn)
		return runQueryWithLabels(with, getLabels(skip+1), conn)
	}
}

type withConn func(context.Context, conn) error

func (i instance) withConn(with withConn, write bool) error {
	return i.p.withConn(with, write, 1)
}

func (i instance) getConn() *sqlite.Conn {
	return i.p.pool.Get(context.TODO())
}

func (i instance) putConn(conn *sqlite.Conn) {
	i.p.pool.Put(conn)
}

func (i instance) Readdirnames() (names []string, err error) {
	prefix := i.location + "/"
	err = i.withConn(func(_ context.Context, conn conn) error {
		return sqlitex.Exec(conn, "select name from blob where name like ?", func(stmt *sqlite.Stmt) error {
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
	if conn == nil {
		panic("nil sqlite conn")
	}
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

func (i instance) PutSized(reader io.Reader, size int64) (err error) {
	err = i.withConn(func(_ context.Context, conn conn) error {
		err := sqlitex.Exec(conn, "insert or replace into blob(name, data) values(?, zeroblob(?))",
			nil,
			i.location, size)
		if err != nil {
			return err
		}
		blob, err := i.openBlob(conn, true, false)
		if err != nil {
			return err
		}
		defer blob.Close()
		_, err = io.Copy(blob, reader)
		return err
	}, true)
	return
}

func (i instance) Put(reader io.Reader) (err error) {
	var buf bytes.Buffer
	_, err = io.Copy(&buf, reader)
	if err != nil {
		return err
	}
	if false {
		return i.PutSized(&buf, int64(buf.Len()))
	} else {
		return i.withConn(func(_ context.Context, conn conn) error {
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
			return err
		}, true)
	}
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
	err = i.withConn(func(_ context.Context, conn conn) error {
		var blob *sqlite.Blob
		blob, err = i.openBlob(conn, false, false)
		if err != nil {
			return err
		}
		defer blob.Close()
		ret = fileInfo{blob.Size()}
		return nil
	}, false)
	return
}

func (i instance) ReadAt(p []byte, off int64) (n int, err error) {
	err = i.withConn(func(_ context.Context, conn conn) error {
		if false {
			var blob *sqlite.Blob
			blob, err = i.openBlob(conn, false, true)
			if err != nil {
				return err
			}
			defer blob.Close()
			if off >= blob.Size() {
				err = io.EOF
				return err
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
				return err
			}
			if !gotRow {
				err = errors.New("blob not found")
				return err
			}
			if n < len(p) {
				err = io.EOF
			}
		}
		return nil
	}, false)
	return
}

func (i instance) WriteAt(bytes []byte, i2 int64) (int, error) {
	panic("implement me")
}

func (i instance) Delete() error {
	return i.withConn(func(_ context.Context, conn conn) error {
		return sqlitex.Exec(conn, "delete from blob where name=?", nil, i.location)
	}, true)
}
