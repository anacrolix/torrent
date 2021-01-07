package torrent

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"

	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/mse"
)

// Wraps a raw connection and provides the interface we want for using the
// connection in the message loop.
type deadlineReader struct {
	nc net.Conn
	r  io.Reader
}

func (r deadlineReader) Read(b []byte) (int, error) {
	// Keep-alives should be received every 2 mins. Give a bit of gracetime.
	err := r.nc.SetReadDeadline(time.Now().Add(150 * time.Second))
	if err != nil {
		return 0, fmt.Errorf("error setting read deadline: %s", err)
	}
	return r.r.Read(b)
}

func handleEncryption(
	rw io.ReadWriter,
	skeys mse.SecretKeyIter,
	policy HeaderObfuscationPolicy,
	selector mse.CryptoSelector,
) (
	ret io.ReadWriter,
	headerEncrypted bool,
	cryptoMethod mse.CryptoMethod,
	err error,
) {
	if !policy.RequirePreferred || !policy.Preferred {
		var protocol [len(pp.Protocol)]byte
		if _, err = io.ReadFull(rw, protocol[:]); err != nil {
			return ret, headerEncrypted, cryptoMethod, err
		}

		rw = struct {
			io.Reader
			io.Writer
		}{
			io.MultiReader(bytes.NewReader(protocol[:]), rw),
			rw,
		}
		if string(protocol[:]) == pp.Protocol {
			ret = rw
			return
		}
		if policy.RequirePreferred {
			err = fmt.Errorf("unexpected protocol string %q and header obfuscation disabled", protocol)
			return
		}
	}
	headerEncrypted = true
	ret, cryptoMethod, err = mse.ReceiveHandshake(rw, skeys, selector)
	return
}

// PeerExtensionBits define what extensions are available.
type PeerExtensionBits = pp.ExtensionBits
