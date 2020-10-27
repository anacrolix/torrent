package sqliteStorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/v2/resource"
)

type conn = *sqlite.Conn

func initConn(conn conn) error {
	return sqlitex.ExecTransient(conn, `pragma synchronous=off`, nil)
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
	select * from (select (select sum(length(data)) from blob) as usage_with, last_used, rowid, length(data) from blob order by last_used, rowid limit 1)
		where usage_with >= (select value from setting where name='capacity')
	union all
	select usage_with-data_length, blob.last_used, blob.rowid, length(data) from excess join blob
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

// Emulates a pool from a single Conn.
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

func NewProvider(conn *sqlite.Conn) (_ *provider, err error) {
	err = initConn(conn)
	if err != nil {
		return
	}
	err = initSchema(conn)
	return &provider{&poolFromConn{conn: conn}}, err
}

func NewProviderPool(pool *sqlitex.Pool, numConns int) (_ *provider, err error) {
	_, err = initPoolConns(context.TODO(), pool, numConns)
	if err != nil {
		return
	}
	conn := pool.Get(context.TODO())
	defer pool.Put(conn)
	err = initSchema(conn)
	return &provider{pool: pool}, err
}

func initPoolConns(ctx context.Context, pool *sqlitex.Pool, numConn int) (numInited int, err error) {
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
		err = initConn(conn)
		if err != nil {
			err = fmt.Errorf("initing conn %v: %w", len(conns), err)
			return
		}
		numInited++
	}
	return
}

type pool interface {
	Get(context.Context) conn
	Put(conn)
}

type provider struct {
	pool pool
}

func (p *provider) NewInstance(s string) (resource.Instance, error) {
	return instance{s, p}, nil
}

type instance struct {
	location string
	p        *provider
}

func (i instance) withConn(with func(conn conn)) {
	conn := i.p.pool.Get(context.TODO())
	defer i.p.pool.Put(conn)
	with(conn)
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
	})
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
		err = sqlitex.Exec(conn, "insert or replace into blob(name, data) values(?, ?)", nil, i.location, buf.Bytes())
	})
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
	})
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
	})
	return
}

func (i instance) WriteAt(bytes []byte, i2 int64) (int, error) {
	panic("implement me")
}

func (i instance) Delete() (err error) {
	i.withConn(func(conn conn) {
		err = sqlitex.Exec(conn, "delete from blob where name=?", nil, i.location)
	})
	return
}
