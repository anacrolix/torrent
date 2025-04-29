package storage

import (
	"os"
)

// Default file permissions for writable OS files.
const (
	filePerm os.FileMode = 0o666
	dirPerm  os.FileMode = 0o777
)
