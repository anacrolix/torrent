// +build !cgo disable_libutp

package utpx

import (
	"github.com/anacrolix/utp"
)

// New UTP Socket
func New(network, addr string) (Socket, error) {
	s, err := utp.NewSocket(network, addr)
	if s == nil {
		return nil, err
	} else {
		return s, err
	}
}
