//go:build unix

package storage

import (
	"io"
	"os"

	"github.com/edsrzf/mmap-go"
	"golang.org/x/sys/unix"
)

func seekData(f *os.File, offset int64) (ret int64, err error) {
	ret, err = unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
	// TODO: Handle filesystems that don't support sparse files.
	if err == unix.ENXIO {
		// File has no more data. Treat as short write like io.CopyN.
		err = io.EOF
	}
	return
}

func msync(mm mmap.MMap, offset, nbytes int) error {
	return unix.Msync(mm[offset:offset+nbytes], unix.MS_SYNC)
}
