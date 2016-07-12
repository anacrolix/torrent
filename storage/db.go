package storage

import (
	"database/sql"
	"path/filepath"

	"github.com/anacrolix/torrent/metainfo"
)

type dbPieceCompletion struct {
	db *sql.DB
}

func newDBPieceCompletion(dir string) (ret *dbPieceCompletion, err error) {
	p := filepath.Join(dir, ".torrent.db")
	db, err := sql.Open("sqlite3", p)
	if err != nil {
		return
	}
	_, err = db.Exec(`create table if not exists completed(infohash, "index", unique(infohash, "index") on conflict ignore)`)
	if err != nil {
		db.Close()
		return
	}
	ret = &dbPieceCompletion{db}
	return
}

func (me *dbPieceCompletion) Get(p metainfo.Piece) (ret bool, err error) {
	row := me.db.QueryRow(`select exists(select * from completed where infohash=? and "index"=?)`, p.Info.Hash().HexString(), p.Index())
	err = row.Scan(&ret)
	return
}

func (me *dbPieceCompletion) Set(p metainfo.Piece, b bool) (err error) {
	if b {
		_, err = me.db.Exec(`insert into completed (infohash, "index") values (?, ?)`, p.Info.Hash().HexString(), p.Index())
	} else {
		_, err = me.db.Exec(`delete from completed where infohash=? and "index"=?`, p.Info.Hash().HexString(), p.Index())
	}
	return
}

func (me *dbPieceCompletion) Close() {
	me.db.Close()
}
