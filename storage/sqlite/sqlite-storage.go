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

type InitConnOpts struct {
	SetSynchronous int
	SetJournalMode string
	MmapSizeOk     bool  // If false, a package-specific default will be used.
	MmapSize       int64 // If MmapSizeOk is set, use sqlite default if < 0, otherwise this value.
}

type UnexpectedJournalMode struct {
	JournalMode string
}

func (me UnexpectedJournalMode) Error() string {
	return fmt.Sprintf("unexpected journal mode: %q", me.JournalMode)
}

func setSynchronous(conn conn, syncInt int) (err error) {
	err = sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma synchronous=%v`, syncInt), nil)
	if err != nil {
		return err
	}
	var (
		actual   int
		actualOk bool
	)
	err = sqlitex.ExecTransient(conn, `pragma synchronous`, func(stmt *sqlite.Stmt) error {
		actual = stmt.ColumnInt(0)
		actualOk = true
		return nil
	})
	if err != nil {
		return
	}
	if !actualOk {
		return errors.New("synchronous setting query didn't return anything")
	}
	if actual != syncInt {
		return fmt.Errorf("set synchronous %q, got %q", syncInt, actual)
	}
	return nil
}

func initConn(conn conn, opts InitConnOpts) (err error) {
	err = setSynchronous(conn, opts.SetSynchronous)
	if err != nil {
		return
	}
	// Recursive triggers are required because we need to trim the blob_meta size after trimming to
	// capacity. Hopefully we don't hit the recursion limit, and if we do, there's an error thrown.
	err = sqlitex.ExecTransient(conn, "pragma recursive_triggers=on", nil)
	if err != nil {
		return err
	}
	if opts.SetJournalMode != "" {
		err = sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma journal_mode=%s`, opts.SetJournalMode), func(stmt *sqlite.Stmt) error {
			ret := stmt.ColumnText(0)
			if ret != opts.SetJournalMode {
				return UnexpectedJournalMode{ret}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if !opts.MmapSizeOk {
		// Set the default. Currently it seems the library picks reasonable defaults, especially for
		// wal.
		opts.MmapSize = -1
		//opts.MmapSize = 1 << 24 // 8 MiB
	}
	if opts.MmapSize >= 0 {
		err = sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma mmap_size=%d`, opts.MmapSize), nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func setPageSize(conn conn, pageSize int) error {
	if pageSize == 0 {
		return nil
	}
	var retSize int64
	err := sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma page_size=%d`, pageSize), nil)
	if err != nil {
		return err
	}
	err = sqlitex.ExecTransient(conn, "pragma page_size", func(stmt *sqlite.Stmt) error {
		retSize = stmt.ColumnInt64(0)
		return nil
	})
	if err != nil {
		return err
	}
	if retSize != int64(pageSize) {
		return fmt.Errorf("requested page size %v but got %v", pageSize, retSize)
	}
	return nil
}

func InitSchema(conn conn, pageSize int, triggers bool) error {
	err := setPageSize(conn, pageSize)
	if err != nil {
		return fmt.Errorf("setting page size: %w", err)
	}
	err = sqlitex.ExecScript(conn, `
		-- We have to opt into this before creating any tables, or before a vacuum to enable it. It means we
		-- can trim the database file size with partial vacuums without having to do a full vacuum, which 
		-- locks everything.
		pragma auto_vacuum=incremental;
		
		create table if not exists blob (
			name text,
			last_used timestamp default (datetime('now')),
			data blob,
			verified bool,
			primary key (name)
		);
		
		create table if not exists blob_meta (
			key text primary key,
			value
		);

		create index if not exists blob_last_used on blob(last_used);
		
		-- While sqlite *seems* to be faster to get sum(length(data)) instead of 
		-- sum(length(data)), it may still require a large table scan at start-up or with a 
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
					length(data)
				from blob order by last_used, rowid limit 1
			)
			where usage_with > (select value from setting where name='capacity')
			union all
			select 
				usage_with-data_length as new_usage_with,
				blob.last_used,
				blob.rowid,
				length(data)
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
	InitDbOpts
	ProvOpts    func(*ProviderOpts)
	StorageOpts func(*storage.ResourcePiecesOpts)
}

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewPiecesStorage(opts NewPiecesStorageOpts) (_ storage.ClientImplCloser, err error) {
	conns, err := NewPool(opts.NewPoolOpts)
	if err != nil {
		return
	}
	if opts.PageSize == 0 {
		opts.PageSize = 1 << 14
	}
	err = initPoolDatabase(conns, opts.InitDbOpts)
	if err != nil {
		conns.Close()
		return
	}
	if opts.SetJournalMode == "" && !opts.Memory {
		opts.SetJournalMode = "wal"
	}
	err = initPoolConns(nil, conns, opts.InitConnOpts)
	if err != nil {
		conns.Close()
		return
	}
	provOpts := ProviderOpts{
		BatchWrites: conns.NumConns() > 1,
	}
	if f := opts.ProvOpts; f != nil {
		f(&provOpts)
	}
	prov, err := NewProvider(conns, provOpts)
	if err != nil {
		conns.Close()
		return
	}
	var (
		journalMode string
	)
	withPoolConn(conns, func(c conn) {
		err = sqlitex.Exec(c, "pragma journal_mode", func(stmt *sqlite.Stmt) error {
			journalMode = stmt.ColumnText(0)
			return nil
		})
	})
	if err != nil {
		err = fmt.Errorf("getting journal mode: %w", err)
		prov.Close()
		return
	}
	if journalMode == "" {
		err = errors.New("didn't get journal mode")
		prov.Close()
		return
	}
	storageOpts := storage.ResourcePiecesOpts{
		NoSizedPuts: journalMode != "wal" || conns.NumConns() == 1,
	}
	if f := opts.StorageOpts; f != nil {
		f(&storageOpts)
	}
	store := storage.NewResourcePiecesOpts(prov, storageOpts)
	return struct {
		storage.ClientImpl
		io.Closer
	}{
		store,
		prov,
	}, nil
}

type NewPoolOpts struct {
	NewConnOpts
	InitConnOpts
	NumConns int
}

type InitDbOpts struct {
	DontInitSchema bool
	PageSize       int
	// If non-zero, overrides the existing setting.
	Capacity   int64
	NoTriggers bool
}

// There's some overlap here with NewPoolOpts, and I haven't decided what needs to be done. For now,
// the fact that the pool opts are a superset, means our helper NewPiecesStorage can just take the
// top-level option type.
type PoolConf struct {
	NumConns    int
	JournalMode string
}

// Remove any capacity limits.
func UnlimitCapacity(conn conn) error {
	return sqlitex.Exec(conn, "delete from setting where key='capacity'", nil)
}

// Set the capacity limit to exactly this value.
func SetCapacity(conn conn, cap int64) error {
	return sqlitex.Exec(conn, "insert into setting values ('capacity', ?)", nil, cap)
}

type NewConnOpts struct {
	// See https://www.sqlite.org/c3ref/open.html. NB: "If the filename is an empty string, then a
	// private, temporary on-disk database will be created. This private database will be
	// automatically deleted as soon as the database connection is closed."
	Path   string
	Memory bool
	// Whether multiple blobs will not be read simultaneously. Enables journal mode other than WAL,
	// and NumConns < 2.
	NoConcurrentBlobReads bool
}

func newOpenUri(opts NewConnOpts) string {
	path := url.PathEscape(opts.Path)
	if opts.Memory {
		path = ":memory:"
	}
	values := make(url.Values)
	if opts.NoConcurrentBlobReads || opts.Memory {
		values.Add("cache", "shared")
	}
	return fmt.Sprintf("file:%s?%s", path, values.Encode())
}

func initDatabase(conn conn, opts InitDbOpts) (err error) {
	if !opts.DontInitSchema {
		err = InitSchema(conn, opts.PageSize, !opts.NoTriggers)
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
	return
}

func initPoolDatabase(pool ConnPool, opts InitDbOpts) (err error) {
	withPoolConn(pool, func(c conn) {
		err = initDatabase(c, opts)
	})
	return
}

// Go fmt, why you so shit?
const openConnFlags = 0 |
	sqlite.SQLITE_OPEN_READWRITE |
	sqlite.SQLITE_OPEN_CREATE |
	sqlite.SQLITE_OPEN_URI |
	sqlite.SQLITE_OPEN_NOMUTEX

func newConn(opts NewConnOpts) (conn, error) {
	return sqlite.OpenConn(newOpenUri(opts), openConnFlags)
}

type poolWithNumConns struct {
	*sqlitex.Pool
	numConns int
}

func (me poolWithNumConns) NumConns() int {
	return me.numConns
}

func NewPool(opts NewPoolOpts) (_ ConnPool, err error) {
	if opts.NumConns == 0 {
		opts.NumConns = runtime.NumCPU()
	}
	switch opts.NumConns {
	case 1:
		conn, err := newConn(opts.NewConnOpts)
		return &poolFromConn{conn: conn}, err
	default:
		_pool, err := sqlitex.Open(newOpenUri(opts.NewConnOpts), openConnFlags, opts.NumConns)
		return poolWithNumConns{_pool, opts.NumConns}, err
	}
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

func (poolFromConn) NumConns() int { return 1 }

type ProviderOpts struct {
	BatchWrites bool
}

// Needs the ConnPool size so it can initialize all the connections with pragmas. Takes ownership of
// the ConnPool (since it has to initialize all the connections anyway).
func NewProvider(pool ConnPool, opts ProviderOpts) (_ *provider, err error) {
	prov := &provider{pool: pool, opts: opts}
	if opts.BatchWrites {
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

type InitPoolOpts struct {
	NumConns int
	InitConnOpts
}

func initPoolConns(ctx context.Context, pool ConnPool, opts InitConnOpts) (err error) {
	var conns []conn
	defer func() {
		for _, c := range conns {
			pool.Put(c)
		}
	}()
	for range iter.N(pool.NumConns()) {
		conn := pool.Get(ctx)
		if conn == nil {
			break
		}
		conns = append(conns, conn)
		err = initConn(conn, opts)
		if err != nil {
			err = fmt.Errorf("initing conn %v: %w", len(conns), err)
			return
		}
	}
	return
}

type ConnPool interface {
	Get(context.Context) conn
	Put(conn)
	Close() error
	NumConns() int
}

func withPoolConn(pool ConnPool, with func(conn)) {
	c := pool.Get(nil)
	defer pool.Put(c)
	with(c)
}

type provider struct {
	pool     ConnPool
	writes   chan<- writeRequest
	opts     ProviderOpts
	closeMu  sync.RWMutex
	closed   bool
	closeErr error
}

var _ storage.ConsecutiveChunkReader = (*provider)(nil)

func (p *provider) ReadConsecutiveChunks(prefix string) (io.ReadCloser, error) {
	p.closeMu.RLock()
	runner, err := p.getReadWithConnRunner()
	if err != nil {
		p.closeMu.RUnlock()
		return nil, err
	}
	r, w := io.Pipe()
	go func() {
		defer p.closeMu.RUnlock()
		err = runner(func(_ context.Context, conn conn) error {
			var written int64
			err = sqlitex.Exec(conn, `
				select
					data,
					cast(substr(name, ?+1) as integer) as offset
				from blob
				where name like ?||'%'
				order by offset`,
				func(stmt *sqlite.Stmt) error {
					offset := stmt.ColumnInt64(1)
					if offset != written {
						return fmt.Errorf("got chunk at offset %v, expected offset %v", offset, written)
					}
					// TODO: Avoid intermediate buffers here
					r := stmt.ColumnReader(0)
					w1, err := io.Copy(w, r)
					written += w1
					return err
				},
				len(prefix),
				prefix,
			)
			return err
		})
		w.CloseWithError(err)
	}()
	return r, nil
}

func (me *provider) Close() error {
	me.closeMu.Lock()
	defer me.closeMu.Unlock()
	if me.closed {
		return me.closeErr
	}
	if me.writes != nil {
		close(me.writes)
	}
	me.closeErr = me.pool.Close()
	me.closed = true
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
	p.closeMu.RLock()
	// I think we need to check this here because it may not be valid to send to the writes channel
	// if we're already closed. So don't try to move this check into getReadWithConnRunner.
	if p.closed {
		p.closeMu.RUnlock()
		return errors.New("closed")
	}
	if write && p.opts.BatchWrites {
		done := make(chan error)
		p.writes <- writeRequest{
			query:  with,
			done:   done,
			labels: getLabels(skip + 1),
		}
		p.closeMu.RUnlock()
		return <-done
	} else {
		defer p.closeMu.RUnlock()
		runner, err := p.getReadWithConnRunner()
		if err != nil {
			return err
		}
		return runner(with)
	}
}

// Obtains a DB conn and returns a withConn for executing with it. If no error is returned from this
// function, the runner *must* be used or the conn is leaked. You should check the provider isn't
// closed before using this.
func (p *provider) getReadWithConnRunner() (with func(withConn) error, err error) {
	conn := p.pool.Get(context.TODO())
	if conn == nil {
		err = errors.New("couldn't get pool conn")
		return
	}
	with = func(with withConn) error {
		defer p.pool.Put(conn)
		return runQueryWithLabels(with, getLabels(1), conn)
	}
	return
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
				"select substr(data, ?, ?) from blob where name=?",
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
