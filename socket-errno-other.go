//go:build !windows

package torrent

import "syscall"

var unsupportedNetworkErrnos = []error{
	// The host has no support for the address family at all, e.g. a kernel or container without
	// IPv6.
	syscall.EAFNOSUPPORT,
	// The family is supported but no address of it can be bound, e.g. IPv6 is enabled but there is
	// no IPv6 address on any interface.
	syscall.EADDRNOTAVAIL,
}
