//go:build wasm || windows

package storage

import (
	"io"
	"os"
)

// FlushFileBuffers on Windows requires write access.
const fsyncOpenFlag = os.O_WRONLY

func seekData(f *os.File, offset int64) (ret int64, err error) {
	return f.Seek(offset, io.SeekStart)
}
