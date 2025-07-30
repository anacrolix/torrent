package torrent

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func setReusePortSockOpts(fd uintptr) (err error) {
	return windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
}

func setSockNoLinger(fd uintptr) (err error) {
	return syscall.SetsockoptLinger(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_LINGER, &lingerOffVal)
}

func setSockIPTOS(fd uintptr, val int) (err error) {
	return nil
}
