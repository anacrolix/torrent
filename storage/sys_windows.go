package storage

import (
	"github.com/edsrzf/mmap-go"
)

func msync(mm mmap.MMap, offset, nbytes int) error {
	// Fuck you Windows you suck. TODO: Use windows.FlushViewOfFile.
	return mm.Flush()
}
