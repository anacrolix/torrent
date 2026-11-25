package storage

import (
	"os"
)

// Default file permissions for writable OS files.
const (
	filePerm os.FileMode = 0o644
	dirPerm  os.FileMode = 0o755
)

// fileModes is how the file storage creates files and directories, and whether it may change the
// mode of files that already exist.
type fileModes struct {
	file os.FileMode
	dir  os.FileMode
	keep bool
}

var defaultFileModes = fileModes{file: filePerm, dir: dirPerm}
