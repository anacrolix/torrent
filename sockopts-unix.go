//go:build !windows && !wasm

package torrent

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func setReusePortSockOpts(fd uintptr) (err error) {
	// I would use libp2p/go-reuseport to do this here, but no surprise it's
	// implemented incorrectly.

	// Looks like we can get away with just REUSEPORT at least on Darwin, and probably by
	// extension BSDs and Linux.
	if false {
		err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if err != nil {
			return
		}
	}
	err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	return
}

func setSockNoLinger(fd uintptr) (err error) {
	return syscall.SetsockoptLinger(int(fd), syscall.SOL_SOCKET, syscall.SO_LINGER, &lingerOffVal)
}

func setSockIPTOS(fd uintptr, val int) (err error) {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, val)
}
