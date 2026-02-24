package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/anacrolix/missinggo/v2/panicif"
)

// Opens file for write, creating dirs and fixing permissions as necessary.
func openFileExtra(p string, osRdwr int) (f *os.File, err error) {
	panicif.NotZero(osRdwr & ^(os.O_RDONLY | os.O_RDWR | os.O_WRONLY))
	flag := osRdwr | os.O_CREATE
	f, err = os.OpenFile(p, flag, filePerm)
	if err == nil {
		return
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = os.MkdirAll(filepath.Dir(p), dirPerm)
		if err != nil {
			return
		}
	} else if errors.Is(err, fs.ErrPermission) {
		err = os.Chmod(p, filePerm)
		if err != nil {
			return
		}
	} else {
		return
	}
	f, err = os.OpenFile(p, flag, filePerm)
	return
}
