package util

import (
	"sync/atomic"
	"unsafe"
)
// A chan struct{} that should always be closed and not nil.
var closedChEmptyStruct = make(chan struct{})
func init() {
	close(closedChEmptyStruct)
}

// AtomicChanEmptyStruct wraps a chan struct{} with atomic helper methods
type AtomicChanEmptyStruct chan struct{}

// NewAtomicChanEmptyStruct makes a new *AtomicChanEmptyStruct
func NewAtomicChanEmptyStruct() *AtomicChanEmptyStruct {
	ch := AtomicChanEmptyStruct(make(chan struct{}))
	return &ch
}

// NilOrC returns the underlying channel, or nil if the channel is closed
func (ac *AtomicChanEmptyStruct) NilOrC() (ret <-chan struct{}) {
	if c := atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(ac))); c != nil {
		return *(*chan struct{})(unsafe.Pointer(&c))
	}
	return
}

// C gets the unclosed channel if it isn't closed.
// Otherwise, returns a closed channel.
func (ac *AtomicChanEmptyStruct) C() (ret <-chan struct{}) {
	if c := atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(ac))); c != nil {
		return *(*chan struct{})(unsafe.Pointer(&c))
	}
	return closedChEmptyStruct
}

// Closed returns whether the channel is already closed.
// This is more efficient than <-ch.C().
func (ac *AtomicChanEmptyStruct) Closed() (ret bool) {
	return atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(ac))) == nil
}

// Close tries to close the channel and returns whether the channel was already closed.
// Optimized for channels that are not yet closed. Channels that are already closed
// won't be closed again, but it is a tiny bit less efficient to rely on this behavior.
func (ac *AtomicChanEmptyStruct) Close() (wasNotClosed bool) {
	if c := atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(ac)), nil); c != nil {
		close(*(*chan struct{})(unsafe.Pointer(&c)))
		return true
	}
	return
}

// CloseThenReset tries to close the channel then, if it succeeds, reinitializes the channel
// (thereby effectively unsetting/resetting the channel.)
// If the channel is already closed, does nothing and returns false.
// Optimized for unclosed channels rather than closed ones. This function as of Go 1.17 is not inlined.
func (ac *AtomicChanEmptyStruct) CloseThenReset() (wasNotClosed bool) {
	if c := atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(ac)), nil); c != nil {
		close(*(*chan struct{})(unsafe.Pointer(&c)))
		ch := make(chan struct{})
		atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(ac)), *(*unsafe.Pointer)(unsafe.Pointer(&ch)))
		return true
	}
	return
}

// ResetWithClose eagerly reinitializes the channel and closes the old channel if it wasn't already.
// It's best to use this function when the likelihood of the channel being closed is high
// and the contention is low. If contention is high, it's probably better to use CloseThenReset
// which lazily reinitializes the channel (i.e. only if the channel was already closed before).
// Optimized for closed channels rather than unclosed ones.
func (ac *AtomicChanEmptyStruct) ResetWithClose() {
	ch := make(chan struct{})
	if p := atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(ac)), *(*unsafe.Pointer)(unsafe.Pointer(&ch))); p != nil {
		close(*(*chan struct{})(unsafe.Pointer(&p)))
	}
	return
}

// ResetAssumingClosed reinitializes the channel assuming that the channel
// is likely to be already closed. If the channel isn't already closed, does nothing and returns false.
// Otherwise returns true, indicating that the channel was previously closed and has now been reinitialized.
// Inefficient under high contention as allocated channels would be wasted.
func (ac *AtomicChanEmptyStruct) ResetAssumingClosed() (wasClosed bool) {
	ch := make(chan struct{})
	return atomic.CompareAndSwapPointer((*unsafe.Pointer)(unsafe.Pointer(ac)), nil, *(*unsafe.Pointer)(unsafe.Pointer(&ch)))
}

// ResetIfClosed reinitializes the channel and returns true if the channel is already closed.
// More conservative but slower than ResetAssumingClosed, as ResetIfClosed lazily
// initializes a new chan struct{}. Returns false if it fails to reinitialize the channel --
// which can be either due to the channel not being closed yet or losing the CAS race.
func (ac *AtomicChanEmptyStruct) ResetIfClosed() (wasClosed bool) {
	if atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(ac))) == nil {
		ch := make(chan struct{})
		return atomic.CompareAndSwapPointer((*unsafe.Pointer)(unsafe.Pointer(ac)), nil, *(*unsafe.Pointer)(unsafe.Pointer(&ch)))
	}
	return
}


// ChanEmptyStruct is a basic wrapper for chan struct{}
type ChanEmptyStruct chan struct{}

// MakeChanEmptyStruct makes a ChanEmptyStruct
func MakeChanEmptyStruct() ChanEmptyStruct {
	return make(chan struct{})
}

// NilOrC returns the underlying channel, or nil if the channel is closed
func (ch ChanEmptyStruct) NilOrC() (ret <-chan struct{}) {
	return ch
}

// C gets the unclosed channel if it isn't closed.
// Otherwise, returns a closed channel.
func (ch ChanEmptyStruct) C() <-chan struct{} {
	if ch != nil {
		return ch
	}
	return closedChEmptyStruct
}

// Closed returns whether the channel is already closed.
// This is more efficient than <-ch.C().
func (ch ChanEmptyStruct) Closed() bool {
	return ch == nil
}

// Close tries to close the channel and returns whether the channel was already closed.
// This means that it returns true if a close was performed and false if it wasn't.
func (ch *ChanEmptyStruct) Close() (wasNotClosed bool) {
	if *ch != nil {
		close(*ch)
		*ch = nil
		return true
	}
	return
}

// CloseThenReset tries to close the channel and, if it succeeds, reinitializes the channel
// (thereby effectively unsetting/resetting the channel.)
// If the channel is already closed, does nothing and returns false.
func (ch *ChanEmptyStruct) CloseThenReset() (ret bool) {
	if *ch != nil {
		close(*ch)
		*ch = make(chan struct{})
		return true
	}
	return
}

// ResetIfClosed if the channel is already closed, reinitializes the channel and returns true.
// Returns false if the channel is not closed (and thus is not reset).
func (ch *ChanEmptyStruct) ResetIfClosed() (wasClosed bool) {
	if *ch == nil {
		*ch = make(chan struct{})
		return true
	}
	return
}
