package storage

import (
	"context"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/jackc/pgx/v4/pgxpool"
)

type psqlPieceCompletion struct {
	db *pgxpool.Pool
}

var _ PieceCompletion = (*psqlPieceCompletion)(nil)

func NewPsqlPieceCompletion(databaseurl string) (ret *psqlPieceCompletion, err error) {
	db, err := pgxpool.Connect(context.Background(), databaseurl)
	if err != nil {
		return
	}

	_, err = db.Exec(context.Background(), "create table if not exists piece_completion (infohash TEXT,pindex INT,complete BOOLEAN, unique(infohash, pindex))")
	if err != nil {
		db.Close()
		return
	}

	ret = &psqlPieceCompletion{db: db}
	return
}

func (me *psqlPieceCompletion) Get(pk metainfo.PieceKey) (c Completion, err error) {
	row := me.db.QueryRow(context.Background(), `select complete from piece_completion where infohash=$1 and pindex=$2`, pk.InfoHash.HexString(), int(pk.Index))
	err = row.Scan(&c.Complete)
	if err == nil {
		c.Ok = true
	}
	return
}

func (me *psqlPieceCompletion) Set(pk metainfo.PieceKey, b bool) (err error) {
	_, err = me.db.Exec(context.Background(), `insert into piece_completion (infohash, pindex, complete) values ($1,$2,$3) ON CONFLICT(infohash,pindex) DO UPDATE set complete=$3`, pk.InfoHash.HexString(), int(pk.Index), b)
	return
}

func (me *psqlPieceCompletion) Close() error {
	me.db.Close()
	return nil
}
