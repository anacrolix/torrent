package btprotocol

import (
	"bytes"
	"io"

	"github.com/james-lawrence/torrent/mse"
	"github.com/pkg/errors"
)

// EncryptionHandshake encrypt a net.Conn
type EncryptionHandshake struct {
	Keys               mse.SecretKeyIter
	mse.CryptoSelector // available crypto algorithms
}

// Incoming establishes an encrypted connection. if it is unable to establish an encrypted connection.
// it returns a buffed read/writer that contains the previously read data from the input.
// when the encryption handshake is successful, buffed === updated.
func (t EncryptionHandshake) Incoming(rw io.ReadWriter) (updated io.ReadWriter, buffed io.ReadWriter, err error) {
	type buffered struct {
		io.Reader
		io.Writer
	}

	var (
		buf = bytes.NewBuffer(make([]byte, 0, 68))
	)

	// read the bittorrent protocol magic string initially
	// in case this connection is not an encrypted connection.
	if _, err := io.CopyN(buf, rw, 20); err != nil {
		return updated, updated, err
	} else if buf.String() == Protocol {
		return rw, buffered{
			Reader: io.MultiReader(bytes.NewBuffer(buf.Bytes()), rw),
			Writer: rw,
		}, errors.New("unencrypted connection detected")
	}

	teed := io.MultiReader(bytes.NewBuffer(buf.Bytes()), rw)
	buf = bytes.NewBuffer(make([]byte, 0, 68))

	b := buffered{
		Reader: io.TeeReader(teed, buf),
		Writer: rw,
	}

	if updated, _, err = mse.ReceiveHandshake(b, t.Keys, t.CryptoSelector); err != nil {
		return rw, buffered{
			Reader: io.MultiReader(bytes.NewBuffer(buf.Bytes()), rw),
			Writer: rw,
		}, err
	}

	return updated, updated, nil
}

// Outgoing initiates an outgoing encrypted connection.
func (t EncryptionHandshake) Outgoing(rw io.ReadWriter, secret []byte, algo mse.CryptoMethod) (io.ReadWriter, mse.CryptoMethod, error) {
	return mse.InitiateHandshake(rw, secret, nil, algo)
}
