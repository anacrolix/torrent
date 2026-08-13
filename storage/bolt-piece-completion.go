//go:build !noboltdb && !wasm
// +build !noboltdb,!wasm

package storage

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	g "github.com/anacrolix/generics"
	"go.etcd.io/bbolt"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	boltDbCompleteValue   = "c"
	boltDbIncompleteValue = "i"
)

var completionBucketKey = []byte("completion")

type boltPieceCompletion struct {
	db *bbolt.DB
}

func (me *boltPieceCompletion) Persistent() bool {
	return true
}

var _ PieceCompletion = (*boltPieceCompletion)(nil)

func NewBoltPieceCompletion(dir string) (ret PieceCompletion, err error) {
	os.MkdirAll(dir, 0o750)
	p := filepath.Join(dir, ".torrent.bolt.db")
	db, err := bbolt.Open(p, 0o660, &bbolt.Options{
		Timeout: time.Second,
	})
	if err != nil {
		return
	}
	db.NoSync = true
	ret = &boltPieceCompletion{db}
	return
}

func (me *boltPieceCompletion) Get(pk metainfo.PieceKey) (cn Completion, err error) {
	err = me.db.View(func(tx *bbolt.Tx) error {
		cb := tx.Bucket(completionBucketKey)
		if cb == nil {
			return nil
		}
		ih := cb.Bucket(pk.InfoHash[:])
		if ih == nil {
			return nil
		}
		key := boltPieceCompletionKey(pk.Index)
		cn.Ok = true
		switch string(ih.Get(key[:])) {
		case boltDbCompleteValue:
			cn.Complete = true
		case boltDbIncompleteValue:
			cn.Complete = false
		default:
			cn.Ok = false
		}
		return nil
	})
	return
}

func boltPieceCompletionKey(index int) (key [4]byte) {
	binary.BigEndian.PutUint32(key[:], uint32(index))
	return
}

func (me *boltPieceCompletion) Set(pk metainfo.PieceKey, complete g.Option[bool]) error {
	if c, err := me.Get(pk); err == nil && g.OptionFromTuple(c.Complete, c.Ok) == complete {
		return nil
	}
	if !complete.Ok {
		return me.delete(pk)
	}
	err := me.db.Update(func(tx *bbolt.Tx) error {
		c, err := tx.CreateBucketIfNotExists(completionBucketKey)
		if err != nil {
			return fmt.Errorf("creating completion bucket: %w", err)
		}
		ih, err := c.CreateBucketIfNotExists(pk.InfoHash[:])
		if err != nil {
			return fmt.Errorf("creating bucket for infohash %v: %w", pk.InfoHash, err)
		}
		key := boltPieceCompletionKey(pk.Index)
		value := boltDbIncompleteValue
		if complete.Value {
			value = boltDbCompleteValue
		}
		return ih.Put(key[:], []byte(value))
	})
	if err != nil {
		return fmt.Errorf("setting completion for %v: %w", pk, err)
	}
	return nil
}

// Forget the stored state for a piece, so it reads back as unknown.
func (me *boltPieceCompletion) delete(pk metainfo.PieceKey) error {
	err := me.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket(completionBucketKey)
		if c == nil {
			return nil
		}
		ih := c.Bucket(pk.InfoHash[:])
		if ih == nil {
			return nil
		}
		key := boltPieceCompletionKey(pk.Index)
		return ih.Delete(key[:])
	})
	if err != nil {
		return fmt.Errorf("deleting completion for %v: %w", pk, err)
	}
	return nil
}

func (me *boltPieceCompletion) Close() error {
	return me.db.Close()
}
