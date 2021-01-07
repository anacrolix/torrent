package btprotocol

import (
	"bytes"
	"encoding/hex"
	"io"
	"unicode"

	"github.com/pkg/errors"
)

func debug(in []byte) (r string) {
	s := []rune(string(in))
	o := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsSpace(r) {
			var c []rune
			switch r {
			case '\n':
				c = []rune{'\\', 'n'}
			case '\r':
				c = []rune{'\\', 'r'}
			case '\t':
				c = []rune{'\\', 't'}
			case ' ':
				c = []rune{' '}
			}

			o = append(o, c...)
			continue
		}

		if !unicode.IsPrint(r) {
			o = append(o, []rune(hex.EncodeToString([]byte([]byte{byte(r)})))...)
			// o = append(o, []rune(fmt.Sprintf("%U", r))...)
			continue
		}
		o = append(o, r)
	}

	return string(o)
}

// HandshakeMessage writes the handshake into a destination.
type HandshakeMessage struct {
	Extensions [8]byte
}

// WriteTo write the header to the provided writer.
func (t HandshakeMessage) WriteTo(dst io.Writer) (n int64, err error) {
	var buf = make([]byte, 28) // protocol (20) + bits (8)

	written := copy(buf[:20], []byte(Protocol))
	written += copy(buf[20:28], t.Extensions[:])
	// log.Println("WRITING HANDSHAKE MESSAGE", debug(buf))
	nw, err := dst.Write(buf)
	return int64(nw), err
}

// ReadFrom reads a Handshake from a reader
func (t *HandshakeMessage) ReadFrom(src io.Reader) (n int64, err error) {
	var (
		buf  = make([]byte, 28) // protocol (20) + bits (8)
		read int
	)

	if read, err = io.ReadFull(src, buf); err != nil {
		return int64(read), err
	}

	if read != len(buf) {
		return int64(read), errors.Errorf("invalid handshake invalid length")
	}

	if !bytes.HasPrefix(buf, []byte(Protocol)) {
		return int64(read), errors.Errorf("unexpected protocol string %s:%s", Protocol, string(buf))
	}

	copy(t.Extensions[:], buf[20:])

	return int64(read), nil
}

// HandshakeInfoMessage sent after the HandshakeMessage containing the
// peers ID and the info hash.
type HandshakeInfoMessage struct {
	PeerID [20]byte
	Hash   [20]byte
}

// WriteTo write the header to the provided writer.
func (t HandshakeInfoMessage) WriteTo(dst io.Writer) (n int64, err error) {
	var buf = make([]byte, 40) // info (20) + peer (20)

	written := copy(buf[:20], t.Hash[:])
	written += copy(buf[20:], t.PeerID[:])

	nw, err := dst.Write(buf)
	return int64(nw), err
}

// ReadFrom reads a Handshake from a reader
func (t *HandshakeInfoMessage) ReadFrom(src io.Reader) (n int64, err error) {
	var (
		buf  = make([]byte, 40) // info (20) + peer (20)
		read int
	)

	if read, err = io.ReadFull(src, buf); err != nil {
		return int64(read), err
	}

	if read != len(buf) {
		return int64(read), errors.Errorf("invalid handshake invalid length")
	}

	copy(t.Hash[:], buf[:20])
	copy(t.PeerID[:], buf[20:])

	return int64(read), nil
}

// Extension bits for bittorrent protocol
const (
	ExtensionBitDHT      uint = 0  // http://www.bittorrent.org/beps/bep_0005.html
	ExtensionBitExtended      = 20 // http://www.bittorrent.org/beps/bep_0010.html
	ExtensionBitFast          = 2  // http://www.bittorrent.org/beps/bep_0006.html
)

// ExtensionBits used by the Handshake to determine capabilities of a peer.
type ExtensionBits [8]byte

func (pex ExtensionBits) String() string {
	return hex.EncodeToString(pex[:])
}

// NewExtensionBits initiatize extension bits
func NewExtensionBits(bits ...uint) (ret ExtensionBits) {
	for _, b := range bits {
		ret.SetBit(b)
	}

	return ret
}

// SupportsExtended ...
func (pex ExtensionBits) SupportsExtended() bool {
	return pex.GetBit(ExtensionBitExtended)
}

// SupportsDHT ...
func (pex ExtensionBits) SupportsDHT() bool {
	return pex.GetBit(ExtensionBitDHT)
}

// SupportsFast ...
func (pex ExtensionBits) SupportsFast() bool {
	return pex.GetBit(ExtensionBitFast)
}

// SetBit ...
func (pex *ExtensionBits) SetBit(bit uint) {
	pex[7-bit/8] |= 1 << (bit % 8)
}

// GetBit ...
func (pex ExtensionBits) GetBit(bit uint) bool {
	return pex[7-bit/8]&(1<<(bit%8)) != 0
}

// Handshake ...
type Handshake struct {
	Bits   ExtensionBits
	PeerID [20]byte
}

// Outgoing handshake, used to establish a connection to a peer.
func (t Handshake) Outgoing(sock io.ReadWriter, hash [20]byte) (resbits ExtensionBits, res HandshakeInfoMessage, err error) {
	var (
		msg = HandshakeMessage{
			Extensions: t.Bits,
		}
		peering = HandshakeInfoMessage{
			PeerID: t.PeerID,
			Hash:   hash,
		}
	)

	if _, err := msg.WriteTo(sock); err != nil {
		return resbits, res, err
	}

	if _, err := peering.WriteTo(sock); err != nil {
		return resbits, res, err
	}

	if _, err := msg.ReadFrom(sock); err != nil {
		return resbits, res, err
	}

	if _, err := res.ReadFrom(sock); err != nil {
		return resbits, res, err
	}

	if bytes.Compare(res.Hash[:], hash[:]) != 0 {
		return resbits, res, errors.New("invalid handshake - mismatched hash")
	}

	return msg.Extensions, res, err
}

// Incoming handshake, used to accept a connection from a peer.
func (t Handshake) Incoming(sock io.ReadWriter) (pbits ExtensionBits, pinfo HandshakeInfoMessage, err error) {
	var (
		pmsg HandshakeMessage
		msg  = HandshakeMessage{
			Extensions: t.Bits,
		}
	)

	if _, err := pmsg.ReadFrom(sock); err != nil {
		return pbits, pinfo, err
	}

	if _, err := pinfo.ReadFrom(sock); err != nil {
		return pbits, pinfo, err
	}

	if _, err := msg.WriteTo(sock); err != nil {
		return pbits, pinfo, err
	}

	peering := HandshakeInfoMessage{
		PeerID: t.PeerID,
		Hash:   pinfo.Hash,
	}

	if _, err := peering.WriteTo(sock); err != nil {
		return pbits, pinfo, err
	}

	return pmsg.Extensions, pinfo, nil
}
