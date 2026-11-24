// Package actor provides a mailbox that runs functions one at a time, so that the state they touch
// needs no lock. It is the building block for giving each Torrent (or group of Torrents that share
// storage) a single owner.
package actor

import "sync"

// Actor runs posted functions ("turns") one at a time, in the order they were posted. State that
// is only touched from turns needs no other synchronization.
//
// Turns normally run on the Actor's own goroutine. Call runs its function on the calling goroutine
// when the Actor is idle, so that a synchronous query costs about as much as taking a mutex.
type Actor struct {
	// Runs before each batch of turns. While code that hasn't moved to turns still guards the same
	// state with a lock, this takes that lock.
	beforeBatch func()
	// Runs after each batch of turns, before the Actor goes idle or takes the next batch. This is
	// where state changed by the batch is published: waking waiters, flushing writes and so on.
	afterBatch func()

	mu sync.Mutex
	// Turns waiting to run.
	queue []func()
	// A drained queue, kept to reuse its backing array.
	spare []func()
	// A batch of turns, or an inline Call, is running.
	busy bool
	// The goroutine is started by the first turn that can't run inline.
	started bool
	closed  bool
	// The most turns that have been waiting at once.
	maxQueued int
	// Holds a token when the Actor's goroutine should check the queue again.
	wake chan struct{}
	done chan struct{}
}

// New returns an Actor. Either function may be nil.
func New(beforeBatch, afterBatch func()) *Actor {
	return &Actor{
		beforeBatch: beforeBatch,
		afterBatch:  afterBatch,
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
}

// Called with mu held, when a turn has been queued.
func (a *Actor) startLocked() {
	if !a.started {
		a.started = true
		go a.run()
	}
}

func (a *Actor) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Post queues f to run in a later turn and returns without waiting for it. Post never blocks on
// turns, so it is safe to call from inside a turn. Nothing bounds the queue: senders that can
// outrun the Actor must limit how many of their own turns are outstanding. After Close, f is
// dropped.
func (a *Actor) Post(f func()) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.queue = append(a.queue, f)
	a.maxQueued = max(a.maxQueued, len(a.queue))
	a.startLocked()
	// The first turn queued while idle is the only one that needs to wake the goroutine. If the
	// Actor is busy, whoever is running turns checks the queue when it finishes.
	wake := len(a.queue) == 1 && !a.busy
	a.mu.Unlock()
	if wake {
		a.signal()
	}
}

// Call runs f in a turn and waits for it to return. It must not be called from inside a turn of
// the same Actor. It reports false, without running f, if the Actor is closed.
func (a *Actor) Call(f func()) bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	if !a.busy && len(a.queue) == 0 {
		// Nothing is running and nothing is ahead of us, so take the turn on this goroutine.
		a.busy = true
		a.mu.Unlock()
		if a.beforeBatch != nil {
			a.beforeBatch()
		}
		f()
		a.finishBatch()
		return true
	}
	done := make(chan struct{})
	ran := false
	a.queue = append(a.queue, func() {
		f()
		ran = true
		close(done)
	})
	a.maxQueued = max(a.maxQueued, len(a.queue))
	a.startLocked()
	a.mu.Unlock()
	select {
	case <-done:
	case <-a.done:
		// The goroutine has exited. It may have run our turn first.
		select {
		case <-done:
		default:
		}
	}
	return ran
}

func (a *Actor) finishBatch() {
	if a.afterBatch != nil {
		a.afterBatch()
	}
	a.mu.Lock()
	a.busy = false
	wake := len(a.queue) != 0 || a.closed
	a.mu.Unlock()
	if wake {
		a.signal()
	}
}

// Close stops the Actor after the turns already queued have run, and waits for them.
func (a *Actor) Close() {
	a.mu.Lock()
	a.closed = true
	started := a.started
	a.started = true
	a.mu.Unlock()
	if !started {
		// Nothing was ever queued, and nothing can be now.
		close(a.done)
		return
	}
	a.signal()
	<-a.done
}

func (a *Actor) run() {
	defer close(a.done)
	for {
		a.mu.Lock()
		for a.busy || len(a.queue) == 0 {
			if !a.busy && a.closed {
				a.mu.Unlock()
				return
			}
			a.mu.Unlock()
			<-a.wake
			a.mu.Lock()
		}
		batch := a.queue
		a.queue = a.spare[:0]
		a.spare = nil
		a.busy = true
		a.mu.Unlock()
		if a.beforeBatch != nil {
			a.beforeBatch()
		}
		for _, f := range batch {
			f()
		}
		if a.afterBatch != nil {
			a.afterBatch()
		}
		// Don't keep the turns, and what they capture, alive.
		clear(batch)
		a.mu.Lock()
		a.spare = batch
		a.busy = false
		a.mu.Unlock()
	}
}

// Stats describes an Actor's queue.
type Stats struct {
	// Turns waiting to run now, not counting a batch that is running.
	Queued int
	// The most turns that have been waiting at once.
	MaxQueued int
}

func (a *Actor) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Stats{Queued: len(a.queue), MaxQueued: a.maxQueued}
}
