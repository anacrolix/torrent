package data

import (
	"io"

	"github.com/anacrolix/libtorgo/metainfo"
)

type Store interface {
	OpenTorrent(*metainfo.Info) Data
}

type Data interface {
	// OpenSection(off, n int64) (io.ReadCloser, error)
	// ReadAt(p []byte, off int64) (n int, err error)
	// Close()
	WriteAt(p []byte, off int64) (n int, err error)
	WriteSectionTo(w io.Writer, off, n int64) (written int64, err error)
}
