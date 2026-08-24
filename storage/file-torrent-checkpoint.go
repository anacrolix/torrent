package storage

import (
	"errors"
	"io/fs"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	defaultBufferedCheckpointInterval = time.Second
	defaultBufferedCheckpointBytes    = 128 << 20
)

type fileCheckpointTarget struct {
	primary  string
	fallback string
	length   int64
}

type torrentCheckpointBuffer struct {
	t            *fileTorrentImpl
	checkpointer PieceCompletionCheckpointer

	mu sync.Mutex

	pendingPieces map[metainfo.PieceKey]struct{}
	pendingFiles  map[string]fileCheckpointTarget
	pendingBytes  int64

	lastCheckpoint time.Time
}

func newTorrentCheckpointBuffer(
	t *fileTorrentImpl,
	checkpointer PieceCompletionCheckpointer,
) *torrentCheckpointBuffer {
	return &torrentCheckpointBuffer{
		t:              t,
		checkpointer:   checkpointer,
		pendingPieces:  make(map[metainfo.PieceKey]struct{}),
		pendingFiles:   make(map[string]fileCheckpointTarget),
		lastCheckpoint: time.Now(),
	}
}

func (me *torrentCheckpointBuffer) queuePiece(
	key metainfo.PieceKey,
	length int64,
	targets []fileCheckpointTarget,
) error {
	me.mu.Lock()
	if _, ok := me.pendingPieces[key]; !ok {
		me.pendingPieces[key] = struct{}{}
		me.pendingBytes += length
	}
	for _, target := range targets {
		me.pendingFiles[target.primary] = target
	}
	shouldCheckpoint := me.shouldCheckpointLocked()
	me.mu.Unlock()
	if shouldCheckpoint {
		return me.checkpoint()
	}
	return nil
}

func (me *torrentCheckpointBuffer) shouldCheckpointLocked() bool {
	if len(me.pendingPieces) == 0 {
		return false
	}
	if me.pendingBytes >= defaultBufferedCheckpointBytes {
		return true
	}
	return time.Since(me.lastCheckpoint) >= defaultBufferedCheckpointInterval
}

func (me *torrentCheckpointBuffer) checkpoint() error {
	keys, targets := me.takePending()
	if len(keys) == 0 {
		return nil
	}

	for _, target := range targets {
		if err := me.flushTarget(target); err != nil {
			me.restorePending(keys, targets)
			return err
		}
	}
	if err := me.checkpointer.Checkpoint(keys); err != nil {
		me.restorePending(keys, targets)
		return err
	}
	me.mu.Lock()
	me.lastCheckpoint = time.Now()
	me.mu.Unlock()
	return nil
}

func (me *torrentCheckpointBuffer) close() error {
	return me.checkpoint()
}

func (me *torrentCheckpointBuffer) flushTarget(target fileCheckpointTarget) error {
	err := me.t.io.flush(target.primary, 0, target.length)
	if err == nil {
		return nil
	}
	if target.fallback != "" && errors.Is(err, fs.ErrNotExist) {
		return me.t.io.flush(target.fallback, 0, target.length)
	}
	return err
}

func (me *torrentCheckpointBuffer) takePending() ([]metainfo.PieceKey, []fileCheckpointTarget) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if len(me.pendingPieces) == 0 {
		return nil, nil
	}
	keys := make([]metainfo.PieceKey, 0, len(me.pendingPieces))
	for key := range me.pendingPieces {
		keys = append(keys, key)
	}
	targets := make([]fileCheckpointTarget, 0, len(me.pendingFiles))
	for _, target := range me.pendingFiles {
		targets = append(targets, target)
	}
	clear(me.pendingPieces)
	clear(me.pendingFiles)
	me.pendingBytes = 0
	return keys, targets
}

func (me *torrentCheckpointBuffer) restorePending(
	keys []metainfo.PieceKey,
	targets []fileCheckpointTarget,
) {
	me.mu.Lock()
	defer me.mu.Unlock()
	for _, key := range keys {
		if _, ok := me.pendingPieces[key]; !ok {
			me.pendingPieces[key] = struct{}{}
			me.pendingBytes += int64(me.t.info.Piece(key.Index).Length())
		}
	}
	for _, target := range targets {
		me.pendingFiles[target.primary] = target
	}
}
