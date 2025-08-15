package storage

import (
	"io"
	"os"

	"github.com/edsrzf/mmap-go"
)

func seekData(f *os.File, offset int64) (ret int64, err error) {
	return f.Seek(offset, io.SeekStart)
}

func msync(mm mmap.MMap, offset, nbytes int) error {
	// Fuck you Windows you suck. TODO: Use windows.FlushViewOfFile.
	return mm.Flush()
}
