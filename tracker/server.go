package tracker

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker/udp"
)

// This is reserved for stuff like filtering by IP version, avoiding an announcer's IP or key,
// limiting return count, etc.
type GetPeersOpts struct {
	// Negative numbers are not allowed.
	MaxCount generics.Option[uint]
}

type InfoHash = [20]byte

type PeerInfo struct {
	AnnounceAddr
}

type AnnounceAddr = netip.AddrPort

type AnnounceTracker interface {
	TrackAnnounce(ctx context.Context, req udp.AnnounceRequest, addr AnnounceAddr) error
	Scrape(ctx context.Context, infoHashes []InfoHash) ([]udp.ScrapeInfohashResult, error)
	GetPeers(ctx context.Context, infoHash InfoHash, opts GetPeersOpts) ([]PeerInfo, error)
}

type AnnounceHandler struct {
	AnnounceTracker        AnnounceTracker
	UpstreamTrackers       []Client
	UpstreamTrackerUrls    []string
	UpstreamAnnouncePeerId [20]byte

	mu sync.Mutex
	// Operations are only removed when all the upstream peers have been tracked.
	ongoingUpstreamAugmentations map[InfoHash]augmentationOperation
}

type peerSet = map[PeerInfo]struct{}

type augmentationOperation struct {
	// Closed when no more announce responses are pending. finalPeers will contain all the peers
	// seen.
	doneAnnouncing chan struct{}
	// This receives the latest peerSet until doneAnnouncing is closed.
	curPeers chan peerSet
	// This contains the final peerSet after doneAnnouncing is closed.
	finalPeers peerSet
}

func (me augmentationOperation) getCurPeers() (ret peerSet) {
	ret, _ = me.getCurPeersAndDone()
	return
}

func (me augmentationOperation) getCurPeersAndDone() (ret peerSet, done bool) {
	select {
	case ret = <-me.curPeers:
	case <-me.doneAnnouncing:
		ret = me.finalPeers
		done = true
	}
	return
}

// Adds peers from new that aren't in orig. Modifies both arguments.
func addMissing(orig []PeerInfo, new peerSet) {
	for _, peer := range orig {
		delete(new, peer)
	}
	for peer := range new {
		orig = append(orig, peer)
	}
}

func (me *AnnounceHandler) Serve(
	ctx context.Context, req AnnounceRequest, addr AnnounceAddr, opts GetPeersOpts,
) (peers []PeerInfo, err error) {
	err = me.AnnounceTracker.TrackAnnounce(ctx, req, addr)
	if err != nil {
		return
	}
	infoHash := req.InfoHash
	var op generics.Option[augmentationOperation]
	// Grab a handle to any augmentations that are already running.
	me.mu.Lock()
	op.Value, op.Ok = me.ongoingUpstreamAugmentations[infoHash]
	me.mu.Unlock()
	// Apply num_want limit to max count. I really can't tell if this is the right place to do it,
	// but it seems the most flexible.
	if req.NumWant != -1 {
		newCount := uint(req.NumWant)
		if opts.MaxCount.Ok {
			if newCount < opts.MaxCount.Value {
				opts.MaxCount.Value = newCount
			}
		} else {
			opts.MaxCount = generics.Some(newCount)
		}
	}
	peers, err = me.AnnounceTracker.GetPeers(ctx, infoHash, opts)
	if err != nil {
		return
	}
	// Take whatever peers it has ready. If it's finished, it doesn't matter if we do this inside
	// the mutex or not.
	if op.Ok {
		curPeers, done := op.Value.getCurPeersAndDone()
		addMissing(peers, curPeers)
		if done {
			// It doesn't get any better with this operation. Forget it.
			op.Ok = false
		}
	}
	me.mu.Lock()
	// If we didn't have an operation, and don't have enough peers, start one.
	if !op.Ok && len(peers) <= 1 {
		op.Value, op.Ok = me.ongoingUpstreamAugmentations[infoHash]
		if !op.Ok {
			op.Set(me.augmentPeersFromUpstream(req.InfoHash))
			generics.MakeMapIfNilAndSet(&me.ongoingUpstreamAugmentations, infoHash, op.Value)
		}
	}
	me.mu.Unlock()
	// Wait a while for the current operation.
	if op.Ok {
		// Force the augmentation to return with whatever it has if it hasn't completed in a
		// reasonable time.
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		select {
		case <-ctx.Done():
		case <-op.Value.doneAnnouncing:
		}
		cancel()
		addMissing(peers, op.Value.getCurPeers())
	}
	return
}

func (me *AnnounceHandler) augmentPeersFromUpstream(infoHash [20]byte) augmentationOperation {
	announceCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	subReq := AnnounceRequest{
		InfoHash: infoHash,
		PeerId:   me.UpstreamAnnouncePeerId,
		Event:    None,
		Key:      0,
		NumWant:  -1,
		Port:     0,
	}
	peersChan := make(chan []Peer)
	var pendingUpstreams sync.WaitGroup
	for i := range me.UpstreamTrackers {
		client := me.UpstreamTrackers[i]
		url := me.UpstreamTrackerUrls[i]
		pendingUpstreams.Add(1)
		go func() {
			resp, err := client.Announce(announceCtx, subReq, AnnounceOpt{
				UserAgent: "aragorn",
			})
			peersChan <- resp.Peers
			if err != nil {
				log.Levelf(log.Warning, "error announcing to upstream %q: %v", url, err)
			}
		}()
	}
	peersToTrack := make(map[string]Peer)
	go func() {
		pendingUpstreams.Wait()
		cancel()
		close(peersChan)
		log.Levelf(log.Debug, "adding %v distinct peers from upstream trackers", len(peersToTrack))
		for _, peer := range peersToTrack {
			addrPort, ok := peer.ToNetipAddrPort()
			if !ok {
				continue
			}
			trackReq := AnnounceRequest{
				InfoHash: infoHash,
				Event:    Started,
				Port:     uint16(peer.Port),
			}
			copy(trackReq.PeerId[:], peer.ID)
			err := me.AnnounceTracker.TrackAnnounce(context.TODO(), trackReq, addrPort)
			if err != nil {
				log.Levelf(log.Error, "error tracking upstream peer: %v", err)
			}
		}
		me.mu.Lock()
		delete(me.ongoingUpstreamAugmentations, infoHash)
		me.mu.Unlock()
	}()
	curPeersChan := make(chan map[PeerInfo]struct{})
	doneChan := make(chan struct{})
	retPeers := make(map[PeerInfo]struct{})
	go func() {
		defer close(doneChan)
		for {
			select {
			case peers, ok := <-peersChan:
				if !ok {
					return
				}
				voldemort(peers, peersToTrack, retPeers)
				pendingUpstreams.Done()
			case curPeersChan <- copyPeerSet(retPeers):
			}
		}
	}()
	// Take return references.
	return augmentationOperation{
		curPeers:       curPeersChan,
		finalPeers:     retPeers,
		doneAnnouncing: doneChan,
	}
}

func copyPeerSet(orig peerSet) (ret peerSet) {
	ret = make(peerSet, len(orig))
	for k, v := range orig {
		ret[k] = v
	}
	return
}

// Adds peers to trailing containers.
func voldemort(peers []Peer, toTrack map[string]Peer, sets ...map[PeerInfo]struct{}) {
	for _, protoPeer := range peers {
		toTrack[protoPeer.String()] = protoPeer
		addr, ok := netip.AddrFromSlice(protoPeer.IP)
		if !ok {
			continue
		}
		handlerPeer := PeerInfo{netip.AddrPortFrom(addr, uint16(protoPeer.Port))}
		for _, set := range sets {
			set[handlerPeer] = struct{}{}
		}
	}
}
