// +build !cgo

package torrent

import (
	"github.com/anacrolix/utp"
)

func NewUtpSocket(network, addr string) (utpSocket, error) {
	return utp.NewSocket(network, addr)
}
