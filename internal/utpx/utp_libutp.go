// +build cgo,!disable_libutp

package utpx

import (
	utp "github.com/anacrolix/go-libutp"
)

// New ...
func New(network, addr string) (Socket, error) {
	s, err := utp.NewSocket(network, addr)
	if s == nil {
		return nil, err
	}
	if err != nil {
		return s, err
	}

	return s, err
}
