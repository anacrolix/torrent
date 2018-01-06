package torrent

import (
	"io"

	"github.com/anacrolix/missinggo"
)

type fileReaderInherited interface {
	io.Closer
	SetReadahead(int64)
	SetResponsive()
}

type fileReader struct {
	missinggo.ReadSeekContexter
	fileReaderInherited
}
