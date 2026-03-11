//go:build !windows

// Package torrentfs exposes a torrent client as a read-only filesystem.
//
// It is FUSE-library-agnostic: the core types (TorrentFS, Backend, Unmounter)
// and helpers live here; concrete FUSE backends are in separate modules:
//
//   - github.com/anacrolix/og-torrentfs  – uses github.com/anacrolix/fuse
//   - github.com/anacrolix/hanwen-torrentfs – uses github.com/hanwen/go-fuse/v2
//
// # Filesystem traversal
//
// traverse.go provides the directory-listing and lookup helpers used by every
// backend:
//
//   - RootEntries / RootLookup  – enumerate/find top-level torrent entries
//   - DirEntries / DirLookup    – enumerate/find entries inside a torrent directory
//
// # File reading
//
// fileread.go provides ReadFile, which reads a range of bytes from a torrent
// file.  It blocks until data is available, the context is cancelled, or
// Destroy is called on the TorrentFS.
//
// # Testing
//
// The tfstest sub-package contains a shared integration test suite
// (RunTestSuite) that any backend can run against its MountFunc.
package torrentfs

import (
	"context"
	"strings"
	"sync"

	"github.com/anacrolix/torrent"
)

// TorrentFS is the shared state for a torrent-backed filesystem.
// It holds no FUSE-library-specific code; mount it via a Backend.
type TorrentFS struct {
	Client       *torrent.Client
	destroyed    chan struct{}
	mu           sync.Mutex
	blockedReads int
	event        sync.Cond
}

// Backend is implemented by FUSE library integrations (e.g. hanwen-torrentfs,
// og-torrentfs). It mounts a TorrentFS at a directory and returns an Unmounter.
type Backend interface {
	Mount(mountDir string, tfs *TorrentFS) (Unmounter, error)
}

// Unmounter is returned by Backend.Mount and used to tear down the mount.
type Unmounter interface {
	Unmount() error
}

// New creates a TorrentFS backed by the given client.
func New(cl *torrent.Client) *TorrentFS {
	tfs := &TorrentFS{
		Client:    cl,
		destroyed: make(chan struct{}),
	}
	tfs.event.L = &tfs.mu
	return tfs
}

// Destroy signals all blocked reads to abort and marks the FS as destroyed.
func (tfs *TorrentFS) Destroy() {
	tfs.mu.Lock()
	select {
	case <-tfs.destroyed:
	default:
		close(tfs.destroyed)
	}
	tfs.mu.Unlock()
}

// Destroyed returns a channel that is closed when Destroy is called.
func (tfs *TorrentFS) Destroyed() <-chan struct{} {
	return tfs.destroyed
}

// TrackBlockedRead is called by backend read implementations when they block
// waiting for torrent data. The returned func must be called when the read
// completes or is cancelled.
func (tfs *TorrentFS) TrackBlockedRead() (done func()) {
	tfs.mu.Lock()
	tfs.blockedReads++
	tfs.event.Broadcast()
	tfs.mu.Unlock()
	return func() {
		tfs.mu.Lock()
		tfs.blockedReads--
		tfs.event.Broadcast()
		tfs.mu.Unlock()
	}
}

// WaitBlockedReads blocks until at least n read operations are blocked inside
// the filesystem, or until ctx is done. Used by tests.
func (tfs *TorrentFS) WaitBlockedReads(ctx context.Context, n int) {
	// Broadcast on ctx cancellation so the wait loop can exit.
	go func() {
		<-ctx.Done()
		tfs.mu.Lock()
		tfs.event.Broadcast()
		tfs.mu.Unlock()
	}()
	tfs.mu.Lock()
	defer tfs.mu.Unlock()
	for tfs.blockedReads < n && ctx.Err() == nil {
		tfs.event.Wait()
	}
}

// Mount mounts tfs at mountDir using the given backend.
func (tfs *TorrentFS) Mount(mountDir string, b Backend) (Unmounter, error) {
	return b.Mount(mountDir, tfs)
}

// IsSubPath reports whether child is a direct sub-path of parent.
func IsSubPath(parent, child string) bool {
	if parent == "" {
		return len(child) > 0
	}
	if !strings.HasPrefix(child, parent) {
		return false
	}
	extra := child[len(parent):]
	if extra == "" {
		return false
	}
	return extra[0] == '/'
}
