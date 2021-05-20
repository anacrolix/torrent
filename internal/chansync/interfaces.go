package chansync

// Here we'll strongly-type channels to assist correct usage, if possible.

type (
	Signaled <-chan struct{}
	Done     <-chan struct{}
)
