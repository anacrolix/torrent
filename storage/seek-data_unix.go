//go:build unix

package storage

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func seekData(f *os.File, offset int64) (ret int64, err error) {
	ret, err = unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
	if err == unix.ENXIO {
		// File has no more data. Treat as short write like io.CopyN.
		err = io.EOF
	}
	return
}
