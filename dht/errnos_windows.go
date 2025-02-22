package dht

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// See https://github.com/anacrolix/dht/issues/16.
func ignoreReadFromError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case
			windows.WSAENETRESET,
			windows.WSAECONNRESET,
			windows.WSAECONNABORTED,
			windows.WSAECONNREFUSED,
			windows.WSAENETUNREACH,
			windows.WSAETIMEDOUT: // Why does Go have braindead syntax?
			return true
		}
	}
	return false
}
