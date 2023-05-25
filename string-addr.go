package torrent

import "net"

// This adds a net.Addr interface to a string address that has no presumed Network.
type StringAddr string

var _ net.Addr = StringAddr("")

func (StringAddr) Network() string   { return "" }
func (me StringAddr) String() string { return string(me) }
