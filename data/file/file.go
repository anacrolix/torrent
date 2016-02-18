package file

import (
	"io"
	"os"
	"path/filepath"

	"github.com/anacrolix/torrent/metainfo"
)

type data struct {
	info      *metainfo.Info
	loc       string
	completed []bool
}

func TorrentData(md *metainfo.Info, location string) data {
	return data{md, location, make([]bool, md.NumPieces())}
}

func (me data) Close() {}

func (me data) PieceComplete(piece int) bool {
	return me.completed[piece]
}

func (me data) PieceCompleted(piece int) error {
	me.completed[piece] = true
	return nil
}

func (me data) ReadAt(p []byte, off int64) (n int, err error) {
	for _, fi := range me.info.UpvertedFiles() {
		if off >= fi.Length {
			off -= fi.Length
			continue
		}
		n1 := len(p)
		if int64(n1) > fi.Length-off {
			n1 = int(fi.Length - off)
		}
		var f *os.File
		f, err = os.Open(me.fileInfoName(fi))
		if os.IsNotExist(err) {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			return
		}
		n1, err = f.ReadAt(p[:n1], off)
		f.Close()
		if err != nil {
			return
		}
		n += n1
		off = 0
		p = p[n1:]
		if len(p) == 0 {
			return
		}
	}
	err = io.EOF
	return
}

func (me data) WriteAt(p []byte, off int64) (n int, err error) {
	for _, fi := range me.info.UpvertedFiles() {
		if off >= fi.Length {
			off -= fi.Length
			continue
		}
		n1 := len(p)
		if int64(n1) > fi.Length-off {
			n1 = int(fi.Length - off)
		}
		name := me.fileInfoName(fi)
		os.MkdirAll(filepath.Dir(name), 0770)
		var f *os.File
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0660)
		if err != nil {
			return
		}
		n1, err = f.WriteAt(p[:n1], off)
		f.Close()
		if err != nil {
			return
		}
		n += n1
		off = 0
		p = p[n1:]
		if len(p) == 0 {
			break
		}
	}
	return
}

func (me data) fileInfoName(fi metainfo.FileInfo) string {
	return filepath.Join(append([]string{me.loc, me.info.Name}, fi.Path...)...)
}
