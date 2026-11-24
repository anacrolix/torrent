package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplaceSys(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		// The filesystem doesn't support RENAME_EXCL.
		return renameNoReplaceFallback(from, to)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
