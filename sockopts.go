//go:build !wasm

package torrent

import "syscall"

var lingerOffVal = syscall.Linger{
	Onoff:  0,
	Linger: 0,
}
