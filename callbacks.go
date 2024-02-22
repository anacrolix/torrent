package torrent

import (
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// These are called synchronously, and do not pass ownership of arguments (do not expect to retain
// data after returning from the callback). The Client and other locks may still be held. nil
// functions are not called.
type Callbacks struct {
	// Called after a peer connection completes the BitTorrent handshake. The Client lock is not
	// held.
	CompletedHandshake func(*PeerConn, InfoHash)
	ReadMessage        func(*PeerConn, *pp.Message)
	// This can be folded into the general case below.
	ReadExtendedHandshake func(*PeerConn, *pp.ExtendedHandshakeMessage)
	PeerConnClosed        func(*PeerConn)
	// BEP 10 message. Not sure if I should call this Ltep universally. Each handler here is called
	// in order.
	PeerConnReadExtensionMessage []func(PeerConnReadExtensionMessageEvent)

	// Provides secret keys to be tried against incoming encrypted connections.
	ReceiveEncryptedHandshakeSkeys mse.SecretKeyIter

	ReceivedUsefulData []func(ReceivedUsefulDataEvent)
	ReceivedRequested  []func(PeerMessageEvent)
	DeletedRequest     []func(PeerRequestEvent)
	SentRequest        []func(PeerRequestEvent)
	PeerClosed         []func(*Peer)
	NewPeer            []func(*Peer)
	// Called when a PeerConn has been added to a Torrent. It's finished all BitTorrent protocol
	// handshakes, and is about to start sending and receiving BitTorrent messages. The extended
	// handshake has not yet occurred. This is a good time to alter the supported extension
	// protocols.
	PeerConnAdded []func(*PeerConn)
}

type ReceivedUsefulDataEvent = PeerMessageEvent

type PeerMessageEvent struct {
	Peer    *Peer
	Message *pp.Message
}

type PeerRequestEvent struct {
	Peer *Peer
	Request
}

type PeerConnReadExtensionMessageEvent struct {
	PeerConn *PeerConn
	// You can look up what protocol this corresponds to using the PeerConn.LocalLtepProtocolMap.
	ExtensionNumber pp.ExtensionNumber
	Payload         []byte
}
