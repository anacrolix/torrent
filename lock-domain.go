package torrent

import (
	"github.com/RoaringBitmap/roaring/v2"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/storage"
)

// A lockDomain guards the state of the Torrents in one request-order domain: a Torrent on its own,
// or all the Torrents whose storage shares a storage.TorrentCapacity, since those share a piece
// request order.
//
// There are two ways to hold the locks:
//
//   - Exclusive: the Client lock for write (Client.lock). Nothing else runs, as before domains
//     existed. Client-wide state (the torrent registry, dialing, bans, webseed planning, piece
//     hasher limits, tracker dispatch) may only be changed this way.
//   - Domain: the Client lock for read, then one domain's lock (Torrent.lockDomain). Torrents in
//     different domains run concurrently. Only state belonging to the domain's Torrents and their
//     Peers may be changed. Client-wide state may be read, since it only changes in exclusive mode,
//     or changed through its own synchronization. Work that needs exclusive mode is queued with
//     Torrent.deferExclusive and runs after the domain is released.
//
// The lock order is the Client lock, then a domain. Only one domain is held at a time.
type lockDomain struct {
	mu             lockWithDeferreds
	unlockHandlers clientUnlockHandlers
	// Actions that need exclusive mode, run once the domain and the Client read lock are released.
	exclusiveActions []func()
	// Keys of exclusiveActions added with deferExclusiveOnce.
	exclusiveActionKeys map[any]struct{}
	// Scratch space for use while the domain is held.
	roaringIntIterator roaring.IntIterator
}

func (cl *Client) newLockDomain() *lockDomain {
	d := &lockDomain{}
	d.unlockHandlers.init()
	d.mu.client = cl
	d.mu.unlockHandlers = &d.unlockHandlers
	return d
}

// Returns the domain for Torrents with storage capacity c, making it if needed. Domains for a
// capacity are never removed: a closed Torrent's goroutines may still lock its domain, and must
// exclude any Torrent added later with the same capacity.
func (cl *Client) capacityLockDomain(c storage.TorrentCapacity) *lockDomain {
	cl.assertExclusive()
	if d, ok := cl.capacityDomains[c]; ok {
		return d
	}
	d := cl.newLockDomain()
	g.MakeMapIfNil(&cl.capacityDomains)
	cl.capacityDomains[c] = d
	return d
}

// Moves the Torrent into the domain for its storage capacity, if it has one. Called in exclusive
// mode once storage is open, so no goroutine holds the Torrent's previous domain.
func (t *Torrent) assignLockDomain() {
	t.cl.assertExclusive()
	if t.storage == nil || t.storage.Capacity == nil {
		return
	}
	t.domain = t.cl.capacityLockDomain(t.storage.Capacity)
}

// Panics unless the Client lock is held for write.
func (cl *Client) assertExclusive() {
	panicif.False(cl._mu.held)
}

// Takes the Torrent's domain lock in domain mode.
func (t *Torrent) lockDomain() {
	t.cl._mu.internal.RLock()
	// The domain only changes in exclusive mode, so it's stable until the read lock is released.
	d := t.domain
	d.mu.Lock()
}

func (t *Torrent) unlockDomain() {
	cl := t.cl
	d := t.domain
	d.mu.runUnlockActions()
	actions := d.exclusiveActions
	d.exclusiveActions = nil
	d.exclusiveActionKeys = nil
	d.mu.releaseInternal()
	cl._mu.internal.RUnlock()
	if len(actions) == 0 {
		return
	}
	cl.lock()
	for _, f := range actions {
		f()
	}
	cl.unlock()
}

// Reads the Torrent's state. Shared with other readers of the domain.
func (t *Torrent) rLockDomain() {
	t.cl._mu.internal.RLock()
	t.domain.mu.internal.RLock()
}

func (t *Torrent) rUnlockDomain() {
	t.domain.mu.internal.RUnlock()
	t.cl._mu.internal.RUnlock()
}

// Whether the caller holds the Torrent's domain for write, rather than the Client exclusively.
// Must be called holding one or the other.
func (t *Torrent) inDomainMode() bool {
	return t.domain.mu.held
}

// The lock that deferred actions for the Torrent attach to in the current mode.
func (t *Torrent) activeLock() *lockWithDeferreds {
	if t.inDomainMode() {
		return &t.domain.mu
	}
	return &t.cl._mu
}

func (t *Torrent) unlockHandlers() *clientUnlockHandlers {
	if t.inDomainMode() {
		return &t.domain.unlockHandlers
	}
	return &t.cl.unlockHandlers
}

func (t *Torrent) roaringIntIterator() *roaring.IntIterator {
	if t.inDomainMode() {
		return &t.domain.roaringIntIterator
	}
	return &t.cl.roaringIntIterator
}

// Runs f in exclusive mode: now if already exclusive, otherwise once the domain is released. f
// must recheck any state it depends on, since the domain may have changed in between.
func (t *Torrent) deferExclusive(f func()) {
	if t.inDomainMode() {
		t.domain.exclusiveActions = append(t.domain.exclusiveActions, f)
		return
	}
	t.cl.assertExclusive()
	f()
}

// Like deferExclusive, but in domain mode only the first action with a given key is kept until
// the domain is released.
func (t *Torrent) deferExclusiveOnce(key any, f func()) {
	if t.inDomainMode() {
		d := t.domain
		if g.MapContains(d.exclusiveActionKeys, key) {
			return
		}
		g.MakeMapIfNil(&d.exclusiveActionKeys)
		d.exclusiveActionKeys[key] = struct{}{}
	}
	t.deferExclusive(f)
}

type (
	openNewConnsKey                   struct{ t *Torrent }
	startPieceHashersKey              struct{ t *Torrent }
	updateRegularTrackerAnnouncingKey struct{ t *Torrent }
	webseedNeedUpdateRequestsKey      struct{ ws *webseedPeer }
)

// Locks a Torrent's state in domain mode, or the whole Client in exclusive mode. Returned by
// Peer.locker.
type torrentLocker struct {
	t         *Torrent
	exclusive bool
}

func (me torrentLocker) Lock() {
	if me.exclusive {
		me.t.cl.lock()
	} else {
		me.t.lockDomain()
	}
}

func (me torrentLocker) Unlock() {
	if me.exclusive {
		me.t.cl.unlock()
	} else {
		me.t.unlockDomain()
	}
}

// In exclusive mode this takes the Client lock for write: readers holding only the Client read
// lock would race with domain-mode writers.
func (me torrentLocker) RLock() {
	if me.exclusive {
		me.t.cl.lock()
	} else {
		me.t.rLockDomain()
	}
}

func (me torrentLocker) RUnlock() {
	if me.exclusive {
		me.t.cl.unlock()
	} else {
		me.t.rUnlockDomain()
	}
}

func (me torrentLocker) Defer(action func()) {
	me.t.activeLock().Defer(action)
}

func (me torrentLocker) DeferUniqueUnaryFunc(arg any, action func()) {
	me.t.activeLock().DeferUniqueUnaryFunc(arg, action)
}
