//go:build cgo && !nosqlite
// +build cgo,!nosqlite

package storage

import (
	"path/filepath"
	"sync"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"

	"github.com/anacrolix/torrent/metainfo"
)

type sqlitePieceCompletion struct {
	mu sync.Mutex
	db *sqlite.Conn
}

var _ PieceCompletion = (*sqlitePieceCompletion)(nil)

func NewSqlitePieceCompletion(dir string) (ret *sqlitePieceCompletion, err error) {
	p := filepath.Join(dir, ".torrent.db")
	db, err := sqlite.OpenConn(p, 0)
	if err != nil {
		return
	}
	err = sqlitex.ExecScript(db, `create table if not exists piece_completion(infohash, "index", complete, unique(infohash, "index"))`)
	if err != nil {
		db.Close()
		return
	}
	ret = &sqlitePieceCompletion{db: db}
	return
}

func (me *sqlitePieceCompletion) Get(pk metainfo.PieceKey) (c Completion, err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	err = sqlitex.Exec(
		me.db, `select complete from piece_completion where infohash=? and "index"=?`,
		func(stmt *sqlite.Stmt) error {
			c.Complete = stmt.ColumnInt(0) != 0
			c.Ok = true
			return nil
		},
		pk.InfoHash.HexString(), pk.Index)
	return
}

func (me *sqlitePieceCompletion) Set(pk metainfo.PieceKey, b bool) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	return sqlitex.Exec(
		me.db,
		`insert or replace into piece_completion(infohash, "index", complete) values(?, ?, ?)`,
		nil,
		pk.InfoHash.HexString(), pk.Index, b)
}

func (me *sqlitePieceCompletion) Close() (err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.db != nil {
		err = me.db.Close()
		me.db = nil
	}
	return
}
