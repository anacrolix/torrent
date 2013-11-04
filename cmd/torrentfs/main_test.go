package main

import (
	"net"
	"testing"
)

func TestTCPAddrString(t *testing.T) {
	ta := &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 3000,
	}
	s := ta.String()
	l, err := net.Listen("tcp4", "localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ras := c.RemoteAddr().String()
	if ras != s {
		t.FailNow()
	}
}
