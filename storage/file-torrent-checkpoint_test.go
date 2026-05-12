package storage

import (
	"io/fs"
	"log/slog"
	"sync"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

type checkpointCountingFileIo struct {
	flushes []string
	closed  bool
}

func (me *checkpointCountingFileIo) openForSharedRead(string) (sharableReader, error) {
	panic("unexpected openForSharedRead")
}

func (me *checkpointCountingFileIo) openForRead(string) (fileReader, error) {
	panic("unexpected openForRead")
}

func (me *checkpointCountingFileIo) openForWrite(string, int64) (fileWriter, error) {
	panic("unexpected openForWrite")
}

func (me *checkpointCountingFileIo) flush(name string, _, _ int64) error {
	me.flushes = append(me.flushes, name)
	return nil
}

func (me *checkpointCountingFileIo) rename(string, string) error {
	return fs.ErrNotExist
}

func (me *checkpointCountingFileIo) Close() error {
	me.closed = true
	return nil
}

func (me *checkpointCountingFileIo) closeWriters() (closedPaths []string, remaining int, err error) {
	return nil, 0, nil
}

func TestBufferedCheckpointDefersFileFlushUntilTorrentClose(t *testing.T) {
	info := &metainfo.Info{
		Files:       []metainfo.FileInfo{{Path: []string{"a.bin"}, Length: 1}},
		Pieces:      make([]byte, 20),
		PieceLength: 1,
	}
	files := []fileExtra{{
		safeOsPath:       "a.bin",
		remainingPieces:  1,
		remainingKnown:   true,
	}}
	pc := newBufferedPieceCompletion(&recordingPieceCompletion{})
	checkpointer, ok := pc.(PieceCompletionCheckpointer)
	qt.Assert(t, qt.IsTrue(ok))
	ioImpl := &checkpointCountingFileIo{}
	torrent := &fileTorrentImpl{
		info:              info,
		files:             files,
		metainfoFileInfos: info.UpvertedFiles(),
		segmentLocater:    info.FileSegmentsIndex(),
		infoHash:          metainfo.HashBytes([]byte("x")),
		io:                ioImpl,
		client: &fileClientImpl{
			opts: NewFileClientOpts{
				PieceCompletion: pc,
				UsePartFiles:    g.Some(false),
				Logger:          slog.Default(),
			},
		},
	}
	torrent.checkpoint = newTorrentCheckpointBuffer(torrent, checkpointer)

	piece := &filePieceImpl{
		t: torrent,
		p: info.Piece(0),
	}
	qt.Assert(t, qt.IsNil(piece.MarkComplete()))
	qt.Assert(t, qt.HasLen(ioImpl.flushes, 0))

	qt.Assert(t, qt.IsNil(torrent.Close()))
	qt.Assert(t, qt.DeepEquals(ioImpl.flushes, []string{"a.bin"}))

	completion, err := pc.Get(metainfo.PieceKey{
		InfoHash: torrent.infoHash,
		Index:    0,
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(completion.Ok))
	qt.Assert(t, qt.IsTrue(completion.Complete))
	qt.Assert(t, qt.IsTrue(ioImpl.closed))
}

func TestTorrentCheckpointBufferConcurrentQueuePiece(t *testing.T) {
	info := &metainfo.Info{
		PieceLength: 1,
		Pieces:      make([]byte, 20*4),
	}
	pc := newBufferedPieceCompletion(&recordingPieceCompletion{})
	checkpointer, ok := pc.(PieceCompletionCheckpointer)
	qt.Assert(t, qt.IsTrue(ok))
	torrent := &fileTorrentImpl{
		info:     info,
		infoHash: metainfo.HashBytes([]byte("y")),
		io:       &checkpointCountingFileIo{},
	}
	buffer := newTorrentCheckpointBuffer(torrent, checkpointer)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				err := buffer.queuePiece(
					metainfo.PieceKey{
						InfoHash: torrent.infoHash,
						Index:    i*32 + j,
					},
					1,
					[]fileCheckpointTarget{{
						primary: "a.bin",
						length:  1,
					}},
				)
				qt.Assert(t, qt.IsNil(err))
			}
		}(i)
	}
	wg.Wait()
	qt.Assert(t, qt.IsNil(buffer.close()))
}
