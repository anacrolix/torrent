// +build cgo,!nosqlite3

package storage

import (
	"path/filepath"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"

	"github.com/anacrolix/torrent/metainfo"
)

type sqlitePieceCompletion struct {
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
	ret = &sqlitePieceCompletion{db}
	return
}

func (me *sqlitePieceCompletion) Get(pk metainfo.PieceKey) (c Completion, err error) {
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
	return sqlitex.Exec(
		me.db,
		`insert or replace into piece_completion(infohash, "index", complete) values(?, ?, ?)`,
		nil,
		pk.InfoHash.HexString(), pk.Index, b)
}

func (me *sqlitePieceCompletion) Close() error {
	return me.db.Close()
}
