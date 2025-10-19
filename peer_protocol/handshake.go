package peer_protocol

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/bits"
	"strings"
	"unsafe"

	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/internal/ctxrw"
	"github.com/anacrolix/torrent/metainfo"
)

type ExtensionBit uint

// https://www.bittorrent.org/beps/bep_0004.html
// https://wiki.theory.org/BitTorrentSpecification.html#Reserved_Bytes
const (
	ExtensionBitDht  = 0 // http://www.bittorrent.org/beps/bep_0005.html
	ExtensionBitFast = 2 // http://www.bittorrent.org/beps/bep_0006.html
	// A peer connection initiator can set this when sending a v1 infohash during handshake if they
	// allow the receiving end to upgrade to v2 by responding with the corresponding v2 infohash.
	// BEP 52, and BEP 4. TODO: Set by default and then clear it when it's not appropriate to send.
	ExtensionBitV2Upgrade                    = 4
	ExtensionBitAzureusExtensionNegotiation1 = 16
	ExtensionBitAzureusExtensionNegotiation2 = 17
	// LibTorrent Extension Protocol, http://www.bittorrent.org/beps/bep_0010.html
	ExtensionBitLtep = 20
	// https://wiki.theory.org/BitTorrent_Location-aware_Protocol_1
	ExtensionBitLocationAwareProtocol    = 43
	ExtensionBitAzureusMessagingProtocol = 63 // https://www.bittorrent.org/beps/bep_0004.html

)

func handshakeWriter(w io.Writer, bb <-chan []byte, done chan<- error) {
	var err error
	for b := range bb {
		_, err = w.Write(b)
		if err != nil {
			break
		}
	}
	done <- err
}

type (
	PeerExtensionBits [8]byte
)

var bitTags = []struct {
	bit ExtensionBit
	tag string
}{
	// Ordered by their bit position left to right.
	{ExtensionBitAzureusMessagingProtocol, "amp"},
	{ExtensionBitLocationAwareProtocol, "loc"},
	{ExtensionBitLtep, "ltep"},
	{ExtensionBitAzureusExtensionNegotiation2, "azen2"},
	{ExtensionBitAzureusExtensionNegotiation1, "azen1"},
	{ExtensionBitV2Upgrade, "v2"},
	{ExtensionBitFast, "fast"},
	{ExtensionBitDht, "dht"},
}

func (pex PeerExtensionBits) String() string {
	pexHex := hex.EncodeToString(pex[:])
	tags := make([]string, 0, len(bitTags)+1)
	for _, bitTag := range bitTags {
		if pex.GetBit(bitTag.bit) {
			tags = append(tags, bitTag.tag)
			pex.SetBit(bitTag.bit, false)
		}
	}
	unknownCount := bits.OnesCount64(*(*uint64)((unsafe.Pointer(&pex[0]))))
	if unknownCount != 0 {
		tags = append(tags, fmt.Sprintf("%v unknown", unknownCount))
	}
	return fmt.Sprintf("%v (%s)", pexHex, strings.Join(tags, ", "))

}

func NewPeerExtensionBytes(bits ...ExtensionBit) (ret PeerExtensionBits) {
	for _, b := range bits {
		ret.SetBit(b, true)
	}
	return
}

func (pex PeerExtensionBits) SupportsExtended() bool {
	return pex.GetBit(ExtensionBitLtep)
}

func (pex PeerExtensionBits) SupportsDHT() bool {
	return pex.GetBit(ExtensionBitDht)
}

func (pex PeerExtensionBits) SupportsFast() bool {
	return pex.GetBit(ExtensionBitFast)
}

func (pex *PeerExtensionBits) SetBit(bit ExtensionBit, on bool) {
	if on {
		pex[7-bit/8] |= 1 << (bit % 8)
	} else {
		pex[7-bit/8] &^= 1 << (bit % 8)
	}
}

func (pex PeerExtensionBits) GetBit(bit ExtensionBit) bool {
	return pex[7-bit/8]&(1<<(bit%8)) != 0
}

type HandshakeResult struct {
	PeerExtensionBits
	PeerID [20]byte
	metainfo.Hash
}

// ih is nil if we expect the peer to declare the InfoHash, such as when the peer initiated the
// connection. Returns ok if the Handshake was successful, and err if there was an unexpected
// condition other than the peer simply abandoning the Handshake.
func Handshake(
	ctx context.Context,
	sock io.ReadWriter,
	ih *metainfo.Hash,
	peerID [20]byte,
	extensions PeerExtensionBits,
) (
	res HandshakeResult, err error,
) {
	sock = ctxrw.WrapReadWriter(ctx, sock)
	// Bytes to be sent to the peer. Should never block the sender.
	postCh := make(chan []byte, 4)
	// A single error value sent when the writer completes.
	writeDone := make(chan error, 1)
	// Performs writes to the socket and ensures posts don't block.
	go handshakeWriter(sock, postCh, writeDone)

	defer func() {
		close(postCh) // Done writing.
		if err != nil {
			return
		}
		// Wait until writes complete before returning from handshake.
		err = <-writeDone
		if err != nil {
			err = fmt.Errorf("error writing: %w", err)
		}
	}()

	post := func(bb []byte) {
		panicif.SendBlocks(postCh, bb)
	}

	post(protocolBytes())
	post(extensions[:])
	if ih != nil { // We already know what we want.
		post(ih[:])
		post(peerID[:])
	}

	// Putting an array on the heap still escapes.
	b := make([]byte, 68)
	// Read in one hit to avoid potential overhead in underlying reader.
	_, err = io.ReadFull(sock, b[:])
	if err != nil {
		return res, fmt.Errorf("while reading: %w", err)
	}

	p := b[:len(Protocol)]
	// This gets optimized to runtime.memequal
	if string(p) != Protocol {
		return res, fmt.Errorf("unexpected protocol string %q", string(p))
	}
	b = b[len(p):]
	read := func(dst []byte) {
		n := copy(dst, b)
		panicif.NotEq(n, len(dst))
		b = b[n:]
	}
	read(res.PeerExtensionBits[:])
	read(res.Hash[:])
	read(res.PeerID[:])
	panicif.NotEq(len(b), 0)
	// peerExtensions.Add(res.PeerExtensionBits.String(), 1)

	// TODO: Maybe we can just drop peers here if we're not interested. This
	// could prevent them trying to reconnect, falsely believing there was
	// just a problem.
	if ih == nil { // We were waiting for the peer to tell us what they wanted.
		post(res.Hash[:])
		post(peerID[:])
	}

	return
}
