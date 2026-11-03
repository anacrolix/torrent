package torrent

import "golang.org/x/sys/windows"

// Winsock reports its own error codes, which aren't the syscall package's emulated Errno values.
var unsupportedNetworkErrnos = []error{
	windows.WSAEAFNOSUPPORT,
	windows.WSAEADDRNOTAVAIL,
}
