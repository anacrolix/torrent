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
	"github.com/anacrolix/missinggo/v2/resource"
)

type conn = *sqlite.Conn

func initConn(conn conn) error {
	return sqlitex.ExecScript(conn, `
create table if not exists blob(
	name text,
	last_used timestamp default (datetime('now')),
	data blob,
	primary key (name)
);
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

func NewProvider(conn *sqlite.Conn) (*provider, error) {
	err := initConn(conn)
	return &provider{&poolFromConn{conn: conn}}, err
}

func NewProviderPool(pool *sqlitex.Pool) (*provider, error) {
	conn := pool.Get(context.TODO())
	defer pool.Put(conn)
	err := initConn(conn)
	return &provider{pool: pool}, err
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
