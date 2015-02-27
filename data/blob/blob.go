package blob

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/anacrolix/libtorgo/metainfo"
)

const (
	filePerm = 0640
	dirPerm  = 0750
)

type data struct {
	info    *metainfo.Info
	baseDir string
}

func TorrentData(info *metainfo.Info, baseDir string) *data {
	return &data{info, baseDir}
}

func (me *data) pieceHashHex(i int) string {
	return hex.EncodeToString(me.info.Pieces[i*20 : (i+1)*20])
}

func (me *data) Close() {}

func (me *data) ReadAt(p []byte, off int64) (n int, err error) {
	hash := me.pieceHashHex(int(off / me.info.PieceLength))
	f, err := os.Open(me.baseDir + "/complete/" + hash)
	if os.IsNotExist(err) {
		f, err = os.Open(me.baseDir + "/incomplete/" + hash)
		if os.IsNotExist(err) {
			err = io.EOF
			return
		}
		if err != nil {
			return
		}
	} else if err != nil {
		return
	}
	defer f.Close()
	off %= me.info.PieceLength
	return f.ReadAt(p, off)
}

func (me *data) openComplete(piece int) (f *os.File, err error) {
	return os.OpenFile(me.baseDir+"/complete/"+me.pieceHashHex(piece), os.O_RDWR, 0660)
}

func (me *data) WriteAt(p []byte, off int64) (n int, err error) {
	i := int(off / me.info.PieceLength)
	off %= me.info.PieceLength
	for len(p) != 0 {
		_, err = os.Stat(me.baseDir + "/complete/" + me.pieceHashHex(i))
		if err == nil {
			err = errors.New("can't write to completed piece")
			return
		}
		os.MkdirAll(me.baseDir+"/incomplete", 0750)
		var f *os.File
		f, err = os.OpenFile(me.baseDir+"/incomplete/"+me.pieceHashHex(i), os.O_WRONLY|os.O_CREATE, 0640)
		if err != nil {
			return
		}
		p1 := p
		maxN := me.info.Piece(i).Length() - off
		if int64(len(p1)) > maxN {
			p1 = p1[:maxN]
		}
		var n1 int
		n1, err = f.WriteAt(p1, off)
		f.Close()
		n += n1
		if err != nil {
			return
		}
		p = p[n1:]
		off = 0
	}
	return
}

func (me *data) pieceReader(piece int, off int64) (ret io.ReadCloser, err error) {
	hash := me.pieceHashHex(piece)
	f, err := os.Open(me.baseDir + "/complete/" + hash)
	if os.IsNotExist(err) {
		f, err = os.Open(me.baseDir + "/incomplete/" + hash)
		if os.IsNotExist(err) {
			err = io.EOF
			return
		}
		if err != nil {
			return
		}
	} else if err != nil {
		return
	}
	return struct {
		io.Reader
		io.Closer
	}{io.NewSectionReader(f, off, me.info.Piece(piece).Length()-off), f}, nil
}

func (me *data) WriteSectionTo(w io.Writer, off, n int64) (written int64, err error) {
	i := int(off / me.info.PieceLength)
	off %= me.info.PieceLength
	for n != 0 {
		var pr io.ReadCloser
		pr, err = me.pieceReader(i, off)
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return
		}
		var n1 int64
		n1, err = io.CopyN(w, pr, n)
		written += n1
		n -= n1
		if err != nil {
			return
		}
		off = 0
	}
	return
}

func (me *data) incompletePiecePath(piece int) string {
	return filepath.Join(me.baseDir, "incomplete", me.pieceHashHex(piece))
}

func (me *data) completedPiecePath(piece int) string {
	return filepath.Join(me.baseDir, "complete", me.pieceHashHex(piece))
}

func (me *data) PieceCompleted(index int) (err error) {
	var (
		incompletePiecePath = me.incompletePiecePath(index)
		completedPiecePath  = me.completedPiecePath(index)
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
	r := io.TeeReader(io.LimitReader(fSrc, me.info.Piece(index).Length()), hasher)
	_, err = io.Copy(fDst, r)
	if err != nil {
		return
	}
	if !bytes.Equal(hasher.Sum(nil), me.info.Piece(index).Hash()) {
		err = errors.New("piece incomplete")
		os.Remove(completedPiecePath)
		return
	}
	os.Remove(incompletePiecePath)
	return
}

func (me *data) PieceComplete(piece int) bool {
	_, err := os.Stat(me.completedPiecePath(piece))
	return err == nil
}
