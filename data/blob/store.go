package blob

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/anacrolix/libtorgo/metainfo"

	dataPkg "github.com/anacrolix/torrent/data"
)

const (
	filePerm = 0640
	dirPerm  = 0750
)

type store struct {
	baseDir   string
	capacity  int64
	completed map[[20]byte]struct{}
}

func (me *store) OpenTorrent(info *metainfo.Info) dataPkg.Data {
	return &data{info, me}
}

type StoreOption func(*store)

func Capacity(bytes int64) StoreOption {
	return func(s *store) {
		s.capacity = bytes
	}
}

func NewStore(baseDir string, opt ...StoreOption) dataPkg.Store {
	s := &store{baseDir, -1, nil}
	for _, o := range opt {
		o(s)
	}
	s.initCompleted()
	return s
}

func hexStringPieceHashArray(s string) (ret [20]byte) {
	if len(s) != 40 {
		panic(s)
	}
	n, err := hex.Decode(ret[:], []byte(s))
	if err != nil {
		panic(err)
	}
	if n != 20 {
		panic(n)
	}
	return
}

func (me *store) initCompleted() {
	fis, err := me.readCompletedDir()
	if err != nil {
		panic(err)
	}
	me.completed = make(map[[20]byte]struct{}, len(fis))
	for _, fi := range fis {
		if len(fi.Name()) != 40 {
			continue
		}
		me.completed[hexStringPieceHashArray(fi.Name())] = struct{}{}
	}
}

func (me *store) completePieceDirPath() string {
	return filepath.Join(me.baseDir, "complete")
}

func (me *store) path(p metainfo.Piece, completed bool) string {
	return filepath.Join(me.baseDir, func() string {
		if completed {
			return "complete"
		} else {
			return "incomplete"
		}
	}(), fmt.Sprintf("%x", p.Hash()))
}

func sliceToPieceHashArray(b []byte) (ret [20]byte) {
	n := copy(ret[:], b)
	if n != 20 {
		panic(n)
	}
	return
}

func (me *store) pieceComplete(p metainfo.Piece) bool {
	_, ok := me.completed[sliceToPieceHashArray(p.Hash())]
	return ok
}

func (me *store) pieceWrite(p metainfo.Piece) (f *os.File) {
	if me.pieceComplete(p) {
		return
	}
	name := me.path(p, false)
	os.MkdirAll(filepath.Dir(name), dirPerm)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		panic(err)
	}
	return
}

func (me *store) pieceRead(p metainfo.Piece) (f *os.File) {
	f, err := os.Open(me.path(p, true))
	if err == nil {
		return
	}
	if !os.IsNotExist(err) {
		panic(err)
	}
	// Ermahgerd, self heal. This occurs when the underlying data goes
	// missing, likely due to a "cache flush", also known as deleting the
	// files. TODO: Trigger an asynchronous initCompleted.
	delete(me.completed, sliceToPieceHashArray(p.Hash()))
	f, err = os.Open(me.path(p, false))
	if err == nil {
		return
	}
	if !os.IsNotExist(err) {
		panic(err)
	}
	return
}

func (me *store) readCompletedDir() (fis []os.FileInfo, err error) {
	f, err := os.Open(me.completePieceDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return
	}
	fis, err = f.Readdir(-1)
	f.Close()
	return
}

func (me *store) removeCompleted(name string) (err error) {
	err = os.Remove(filepath.Join(me.completePieceDirPath(), name))
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return err
	}
	delete(me.completed, hexStringPieceHashArray(name))
	return
}

type fileInfoSorter struct {
	fis []os.FileInfo
}

func (me fileInfoSorter) Len() int {
	return len(me.fis)
}

func lastTime(fi os.FileInfo) (ret time.Time) {
	ret = fi.ModTime()
	atime := accessTime(fi)
	if atime.After(ret) {
		ret = atime
	}
	return
}

func (me fileInfoSorter) Less(i, j int) bool {
	return lastTime(me.fis[i]).Before(lastTime(me.fis[j]))
}

func (me fileInfoSorter) Swap(i, j int) {
	me.fis[i], me.fis[j] = me.fis[j], me.fis[i]
}

func sortFileInfos(fis []os.FileInfo) {
	sorter := fileInfoSorter{fis}
	sort.Sort(sorter)
}

func (me *store) makeSpace(space int64) error {
	if me.capacity < 0 {
		return nil
	}
	if space > me.capacity {
		return errors.New("space requested exceeds capacity")
	}
	fis, err := me.readCompletedDir()
	if err != nil {
		return err
	}
	var size int64
	for _, fi := range fis {
		size += fi.Size()
	}
	sortFileInfos(fis)
	for size > me.capacity-space {
		me.removeCompleted(fis[0].Name())
		size -= fis[0].Size()
		fis = fis[1:]
	}
	return nil
}

func (me *store) PieceCompleted(p metainfo.Piece) (err error) {
	err = me.makeSpace(p.Length())
	if err != nil {
		return
	}
	var (
		incompletePiecePath = me.path(p, false)
		completedPiecePath  = me.path(p, true)
	)
	fSrc, err := os.Open(incompletePiecePath)
	if err != nil {
		return
	}
	defer fSrc.Close()
	os.MkdirAll(filepath.Dir(completedPiecePath), dirPerm)
	fDst, err := os.OpenFile(completedPiecePath, os.O_EXCL|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return
	}
	defer fDst.Close()
	hasher := sha1.New()
	r := io.TeeReader(io.LimitReader(fSrc, p.Length()), hasher)
	_, err = io.Copy(fDst, r)
	if err != nil {
		return
	}
	if !bytes.Equal(hasher.Sum(nil), p.Hash()) {
		err = errors.New("piece incomplete")
		os.Remove(completedPiecePath)
		return
	}
	os.Remove(incompletePiecePath)
	me.completed[sliceToPieceHashArray(p.Hash())] = struct{}{}
	return
}
