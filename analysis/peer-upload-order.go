package analysis

import (
	"fmt"
	"log"
	"sync"

	"github.com/elliotchance/orderedmap"

	"github.com/anacrolix/torrent"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

type peerData struct {
	requested   *orderedmap.OrderedMap
	haveDeleted map[torrent.Request]bool
}

// Tracks the order that peers upload requests that we've sent them.
type PeerUploadOrder struct {
	mu    sync.Mutex
	peers map[*torrent.Peer]*peerData
}

func (me *PeerUploadOrder) Init() {
	me.peers = make(map[*torrent.Peer]*peerData)
}

func (me *PeerUploadOrder) onNewPeer(p *torrent.Peer) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if _, ok := me.peers[p]; ok {
		panic("already have peer")
	}
	me.peers[p] = &peerData{
		requested:   orderedmap.NewOrderedMap(),
		haveDeleted: make(map[torrent.Request]bool),
	}
}

func (me *PeerUploadOrder) onSentRequest(event torrent.PeerRequestEvent) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if !me.peers[event.Peer].requested.Set(event.Request, nil) {
		panic("duplicate request sent")
	}
}

func (me *PeerUploadOrder) Install(cbs *torrent.Callbacks) {
	cbs.NewPeer = append(cbs.NewPeer, me.onNewPeer)
	cbs.SentRequest = append(cbs.SentRequest, me.onSentRequest)
	cbs.ReceivedRequested = append(cbs.ReceivedRequested, me.onReceivedRequested)
	cbs.DeletedRequest = append(cbs.DeletedRequest, me.deletedRequest)
}

func (me *PeerUploadOrder) report(desc string, req torrent.Request, peer *torrent.Peer) {
	peerConn, ok := peer.TryAsPeerConn()
	var peerId *torrent.PeerID
	if ok {
		peerId = &peerConn.PeerID
	}
	log.Printf("%s: %v, %v", desc, req, peerId)
}

func (me *PeerUploadOrder) onReceivedRequested(event torrent.PeerMessageEvent) {
	req := torrent.Request{
		event.Message.Index,
		torrent.ChunkSpec{
			Begin:  event.Message.Begin,
			Length: pp.Integer(len(event.Message.Piece)),
		},
	}
	makeLogMsg := func(desc string) string {
		peerConn, ok := event.Peer.TryAsPeerConn()
		var peerId *torrent.PeerID
		if ok {
			peerId = &peerConn.PeerID
		}
		return fmt.Sprintf("%s: %q, %v", desc, peerId, req)
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	peerData := me.peers[event.Peer]
	if peerData.requested.Front().Key.(torrent.Request) == req {
		log.Print(makeLogMsg("got next requested piece"))
	} else if _, ok := peerData.requested.Get(req); ok {
		log.Print(makeLogMsg(fmt.Sprintf(
			"got requested piece but not next (previous delete=%v)",
			peerData.haveDeleted[req])))
	} else {
		panic(makeLogMsg("got unrequested piece"))
	}
}

func (me *PeerUploadOrder) deletedRequest(event torrent.PeerRequestEvent) {
	me.mu.Lock()
	defer me.mu.Unlock()
	peerData := me.peers[event.Peer]
	if !peerData.requested.Delete(event.Request) {
		panic("nothing to delete")
	}
	peerData.haveDeleted[event.Request] = true
}
