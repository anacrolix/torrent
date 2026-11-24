package storage

import (
	"errors"
	"io/fs"
	"os"
)

// Renames from to to, failing with an error matching fs.ErrExist if to already exists. Platform
// implementations use an atomic rename where the OS and filesystem support it, and otherwise fall
// back to renameNoReplaceFallback.
func renameNoReplace(from, to string) error {
	return renameNoReplaceSys(from, to)
}

// Portable no-replace rename. A hard link fails if the target exists, and is atomic where it's
// supported. Where hard links aren't supported, check for the target before renaming, which can
// still race with something else creating it.
func renameNoReplaceFallback(from, to string) error {
	err := os.Link(from, to)
	if err == nil {
		return os.Remove(from)
	}
	if errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, err = os.Lstat(to)
	if err == nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: fs.ErrExist}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}
