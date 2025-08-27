//go:build unix

package storage

import (
	"io"
	"os"

	"github.com/edsrzf/mmap-go"
	"golang.org/x/sys/unix"
)

// Returns io.EOF if there's no data after offset. That doesn't mean there isn't zeroes for a sparse
// hole. Note that lseek returns -1 on error.
func seekData(f *os.File, offset int64) (ret int64, err error) {
	ret, err = unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
	// TODO: Handle filesystems that don't support sparse files.
	if err == unix.ENXIO {
		// File has no more data. Treat as short write like io.CopyN.
		err = io.EOF
	}
	return
}

var pageSize = unix.Getpagesize()

func msync(mm mmap.MMap, offset, nbytes int) error {
	getDown := offset % pageSize
	return unix.Msync(mm[offset-getDown:offset+nbytes], unix.MS_SYNC)
}
