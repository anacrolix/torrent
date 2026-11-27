package torrent

import (
	"maps"
	"slices"
	"sync/atomic"

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
	// The most exclusiveActions queued in one hold of the domain.
	maxExclusiveActions int
	// Scratch space for use while the domain is held.
	roaringIntIterator roaring.IntIterator
	// Turns waiting for the domain, newest first. See Torrent.callDomainTurn.
	pendingTurns atomic.Pointer[domainTurn]
	// A goroutine is running pending turns, or is about to.
	combining atomic.Bool
	// Turns pushed but not yet run, and the most there have been at once.
	turnsQueued    atomic.Int64
	maxTurnsQueued atomic.Int64
}

// A function waiting to run with its domain held.
type domainTurn struct {
	f    func()
	next *domainTurn
	// Receives turnRan once f has run, or turnCombine if the waiter should run pending turns.
	wake chan turnWake
}

type turnWake uint8

const (
	turnRan turnWake = iota
	turnCombine
)

// How many times a combiner takes all pending turns before handing the job to a waiter, so the
// combiner's own conn isn't held up indefinitely.
const maxCombineRounds = 4

func (cl *Client) newLockDomain() *lockDomain {
	d := &lockDomain{}
	d.unlockHandlers.init()
	d.mu.client = cl
	d.mu.unlockHandlers = &d.unlockHandlers
	cl.lockDomainsMu.Lock()
	g.MakeMapIfNil(&cl.lockDomains)
	cl.lockDomains[d] = struct{}{}
	cl.lockDomainsMu.Unlock()
	return d
}

// Forgets a domain that nothing will use again, for stats.
func (cl *Client) removeLockDomain(d *lockDomain) {
	cl.lockDomainsMu.Lock()
	delete(cl.lockDomains, d)
	cl.lockDomainsMu.Unlock()
}

// Takes the domain in domain mode.
func (d *lockDomain) lock(cl *Client) {
	cl._mu.internal.RLock()
	d.mu.Lock()
}

func (d *lockDomain) unlock(cl *Client) {
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

// Runs f with the Torrent's domain held, in domain mode, and waits for it. Must be called without
// any lock held.
//
// Callers that find the domain busy don't queue on its mutex. They push f onto the domain's
// pending turns and wait without holding any lock, and whoever is combining runs every pending
// turn in one hold of the domain. This saves a lock handoff, with a goroutine park and wake, per
// turn, and keeps waiters from holding the Client read lock, which an exclusive-mode writer would
// otherwise have to wait out.
func (t *Torrent) callDomainTurn(f func()) {
	for {
		t.cl._mu.internal.RLock()
		d := t.domain
		t.cl._mu.internal.RUnlock()
		moved := false
		d.runTurn(t.cl, func() {
			// The Torrent may have moved to its storage capacity's domain since d was read.
			if t.domain != d {
				moved = true
				return
			}
			f()
		})
		if !moved {
			return
		}
	}
}

func (d *lockDomain) runTurn(cl *Client, f func()) {
	// Nobody is combining, so take the domain and run f straight away.
	if d.pendingTurns.Load() == nil && d.combining.CompareAndSwap(false, true) {
		d.combine(cl, f)
		return
	}
	turn := &domainTurn{f: f, wake: make(chan turnWake, 1)}
	for {
		old := d.pendingTurns.Load()
		turn.next = old
		if d.pendingTurns.CompareAndSwap(old, turn) {
			break
		}
	}
	n := d.turnsQueued.Add(1)
	for {
		m := d.maxTurnsQueued.Load()
		if n <= m || d.maxTurnsQueued.CompareAndSwap(m, n) {
			break
		}
	}
	if d.combining.CompareAndSwap(false, true) {
		d.combine(cl, nil)
	}
	for <-turn.wake == turnCombine {
		d.combine(cl, nil)
	}
}

// Runs first, if not nil, and then pending turns, with the domain held. Called by the goroutine
// that set combining.
func (d *lockDomain) combine(cl *Client, first func()) {
	for {
		d.lock(cl)
		if first != nil {
			first()
			first = nil
		}
		for range maxCombineRounds {
			head := d.pendingTurns.Swap(nil)
			if head == nil {
				break
			}
			// Run in the order the turns were pushed.
			var batch []*domainTurn
			for ; head != nil; head = head.next {
				batch = append(batch, head)
			}
			d.turnsQueued.Add(-int64(len(batch)))
			for i := len(batch) - 1; i >= 0; i-- {
				batch[i].f()
				batch[i].wake <- turnRan
			}
		}
		d.unlock(cl)
		if next := d.pendingTurns.Load(); next != nil {
			// Only the combiner removes pending turns, so next stays pending until it combines.
			next.wake <- turnCombine
			return
		}
		d.combining.Store(false)
		// A turn pushed after the last check would wait forever if nobody combines it.
		if d.pendingTurns.Load() == nil || !d.combining.CompareAndSwap(false, true) {
			return
		}
	}
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
	t.domain.mu.Lock()
}

func (t *Torrent) unlockDomain() {
	t.domain.unlock(t.cl)
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
		d := t.domain
		d.exclusiveActions = append(d.exclusiveActions, f)
		d.maxExclusiveActions = max(d.maxExclusiveActions, len(d.exclusiveActions))
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
	updateRegularTrackerAnnouncingKey struct{ t *Torrent }
	webseedNeedUpdateRequestsKey      struct{ ws *webseedPeer }
)

// Locks a Torrent's state in domain mode, or the whole Client in exclusive mode. Returned by
// Peer.locker.
type torrentLocker struct {
	cl        *Client
	t         *Torrent
	exclusive bool
}

func (me torrentLocker) Lock() {
	if me.exclusive {
		me.cl.lock()
	} else {
		me.t.lockDomain()
	}
}

func (me torrentLocker) Unlock() {
	if me.exclusive {
		me.cl.unlock()
	} else {
		me.t.unlockDomain()
	}
}

// In exclusive mode this takes the Client lock for write: readers holding only the Client read
// lock would race with domain-mode writers.
func (me torrentLocker) RLock() {
	if me.exclusive {
		me.cl.lock()
	} else {
		me.t.rLockDomain()
	}
}

func (me torrentLocker) RUnlock() {
	if me.exclusive {
		me.cl.unlock()
	} else {
		me.t.rUnlockDomain()
	}
}

func (me torrentLocker) Defer(action func()) {
	if me.t == nil {
		me.cl.locker().Defer(action)
		return
	}
	me.t.activeLock().Defer(action)
}

func (me torrentLocker) DeferUniqueUnaryFunc(arg any, action func()) {
	if me.t == nil {
		me.cl.locker().DeferUniqueUnaryFunc(arg, action)
		return
	}
	me.t.activeLock().DeferUniqueUnaryFunc(arg, action)
}

// Whether the Torrent shares its storage capacity's domain, rather than having its own.
func (t *Torrent) hasCapacityDomain() bool {
	return t.storage != nil && t.storage.Capacity != nil && t.cl.capacityDomains[t.storage.Capacity] == t.domain
}

// Adds queue depths for the lock domains to stats. Called with the Client lock held in any mode.
func (cl *Client) addLockDomainStats(stats *ClientStats) {
	cl.lockDomainsMu.Lock()
	ds := slices.Collect(maps.Keys(cl.lockDomains))
	cl.lockDomainsMu.Unlock()
	for _, d := range ds {
		stats.LockDomainTurnsQueued += int(d.turnsQueued.Load())
		stats.LockDomainTurnsQueuedMax = max(stats.LockDomainTurnsQueuedMax, int(d.maxTurnsQueued.Load()))
		d.mu.internal.RLock()
		stats.LockDomainExclusiveActionsMax = max(stats.LockDomainExclusiveActionsMax, d.maxExclusiveActions)
		d.mu.internal.RUnlock()
	}
}
