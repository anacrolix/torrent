package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplaceSys(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		// The kernel or filesystem doesn't support RENAME_NOREPLACE.
		return renameNoReplaceFallback(from, to)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
