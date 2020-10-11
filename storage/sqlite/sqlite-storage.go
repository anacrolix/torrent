package sqliteStorage

import (
	"bytes"
	"errors"
	"io"
	"log"
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
create table if not exists blobs(name, data, primary key (name));
`)
}

func NewProvider(conn *sqlite.Conn) (*provider, error) {
	err := initConn(conn)
	return &provider{conn: conn}, err
}

type provider struct {
	mu   sync.Mutex
	conn conn
}

func (p *provider) NewInstance(s string) (resource.Instance, error) {
	return instance{s, p}, nil
}

type instance struct {
	location string
	p        *provider
}

func (i instance) withConn(with func(conn conn)) {
	i.lockConn()
	defer i.unlockConn()
	with(i.p.conn)
}

func (i instance) lockConn() {
	i.p.mu.Lock()
}

func (i instance) unlockConn() {
	i.p.mu.Unlock()
}

func (i instance) Readdirnames() (names []string, err error) {
	prefix := i.location + "/"
	i.withConn(func(conn conn) {
		err = sqlitex.Exec(conn, "select name from blobs where name like ?", func(stmt *sqlite.Stmt) error {
			names = append(names, stmt.ColumnText(0)[len(prefix):])
			return nil
		}, prefix+"%")
	})
	log.Printf("readdir %q gave %q", i.location, names)
	return
}

func (i instance) getBlobRowid(conn conn) (rowid int64, err error) {
	rows := 0
	err = sqlitex.Exec(conn, "select rowid from blobs where name=?", func(stmt *sqlite.Stmt) error {
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
	i.lockConn()
	blob, err := i.openBlob(i.p.conn, false)
	if err != nil {
		i.unlockConn()
		return
	}
	var once sync.Once
	return connBlob{blob, func() {
		once.Do(i.unlockConn)
	}}, nil
}

func (i instance) openBlob(conn conn, write bool) (*sqlite.Blob, error) {
	rowid, err := i.getBlobRowid(conn)
	if err != nil {
		return nil, err
	}
	return conn.OpenBlob("main", "blobs", "data", rowid, write)
}

func (i instance) Put(reader io.Reader) (err error) {
	var buf bytes.Buffer
	_, err = io.Copy(&buf, reader)
	if err != nil {
		return err
	}
	i.withConn(func(conn conn) {
		err = sqlitex.Exec(conn, "insert or replace into blobs(name, data) values(?, ?)", nil, i.location, buf.Bytes())
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
		blob, err = i.openBlob(conn, false)
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
		blob, err = i.openBlob(conn, false)
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
		err = sqlitex.Exec(conn, "delete from blobs where name=?", nil, i.location)
	})
	return
}
