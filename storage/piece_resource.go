package storage

import (
	"bytes"
	"io"
	"path"
	"sort"
	"strconv"
	"sync"

	"github.com/anacrolix/missinggo/v2/resource"

	"github.com/anacrolix/torrent/metainfo"
)

type piecePerResource struct {
	rp   PieceProvider
	opts ResourcePiecesOpts
}

type ResourcePiecesOpts struct {
	LeaveIncompleteChunks bool
	NoSizedPuts           bool
}

func NewResourcePieces(p PieceProvider) ClientImpl {
	return NewResourcePiecesOpts(p, ResourcePiecesOpts{})
}

func NewResourcePiecesOpts(p PieceProvider, opts ResourcePiecesOpts) ClientImpl {
	return &piecePerResource{
		rp:   p,
		opts: opts,
	}
}

type piecePerResourceTorrentImpl struct {
	piecePerResource
}

func (piecePerResourceTorrentImpl) Close() error {
	return nil
}

func (s piecePerResource) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	return piecePerResourceTorrentImpl{s}, nil
}

func (s piecePerResource) Piece(p metainfo.Piece) PieceImpl {
	return piecePerResourcePiece{
		mp:               p,
		piecePerResource: s,
	}
}

type PieceProvider interface {
	resource.Provider
}

type ConsecutiveChunkWriter interface {
	WriteConsecutiveChunks(prefix string, _ io.Writer) (int64, error)
}

type piecePerResourcePiece struct {
	mp metainfo.Piece
	piecePerResource
}

var _ io.WriterTo = piecePerResourcePiece{}

func (s piecePerResourcePiece) WriteTo(w io.Writer) (int64, error) {
	if ccw, ok := s.rp.(ConsecutiveChunkWriter); ok {
		if s.mustIsComplete() {
			return ccw.WriteConsecutiveChunks(s.completedInstancePath(), w)
		} else {
			return s.writeConsecutiveIncompleteChunks(ccw, w)
		}
	}
	return io.Copy(w, io.NewSectionReader(s, 0, s.mp.Length()))
}

func (s piecePerResourcePiece) writeConsecutiveIncompleteChunks(ccw ConsecutiveChunkWriter, w io.Writer) (int64, error) {
	return ccw.WriteConsecutiveChunks(s.incompleteDirPath()+"/", w)
}

// Returns if the piece is complete. Ok should be true, because we are the definitive source of
// truth here.
func (s piecePerResourcePiece) mustIsComplete() bool {
	completion := s.Completion()
	if !completion.Ok {
		panic("must know complete definitively")
	}
	return completion.Complete
}

func (s piecePerResourcePiece) Completion() Completion {
	fi, err := s.completed().Stat()
	return Completion{
		Complete: err == nil && fi.Size() == s.mp.Length(),
		Ok:       true,
	}
}

type SizedPutter interface {
	PutSized(io.Reader, int64) error
}

func (s piecePerResourcePiece) MarkComplete() error {
	incompleteChunks := s.getChunks()
	r, w := io.Pipe()
	go func() {
		var err error
		if ccw, ok := s.rp.(ConsecutiveChunkWriter); ok {
			_, err = s.writeConsecutiveIncompleteChunks(ccw, w)
		} else {
			_, err = io.Copy(w, io.NewSectionReader(incompleteChunks, 0, s.mp.Length()))
		}
		w.CloseWithError(err)
	}()
	completedInstance := s.completed()
	err := func() error {
		if sp, ok := completedInstance.(SizedPutter); ok && !s.opts.NoSizedPuts {
			return sp.PutSized(r, s.mp.Length())
		} else {
			return completedInstance.Put(r)
		}
	}()
	if err == nil && !s.opts.LeaveIncompleteChunks {
		// I think we do this synchronously here since we don't want callers to act on the completed
		// piece if we're concurrently still deleting chunks. The caller may decide to start
		// downloading chunks again and won't expect us to delete them. It seems to be much faster
		// to let the resource provider do this if possible.
		var wg sync.WaitGroup
		for _, c := range incompleteChunks {
			wg.Add(1)
			go func(c chunk) {
				defer wg.Done()
				c.instance.Delete()
			}(c)
		}
		wg.Wait()
	}
	return err
}

func (s piecePerResourcePiece) MarkNotComplete() error {
	return s.completed().Delete()
}

func (s piecePerResourcePiece) ReadAt(b []byte, off int64) (int, error) {
	if s.mustIsComplete() {
		return s.completed().ReadAt(b, off)
	}
	return s.getChunks().ReadAt(b, off)
}

func (s piecePerResourcePiece) WriteAt(b []byte, off int64) (n int, err error) {
	i, err := s.rp.NewInstance(path.Join(s.incompleteDirPath(), strconv.FormatInt(off, 10)))
	if err != nil {
		panic(err)
	}
	r := bytes.NewReader(b)
	err = i.Put(r)
	n = len(b) - r.Len()
	return
}

type chunk struct {
	offset   int64
	instance resource.Instance
}

type chunks []chunk

func (me chunks) ReadAt(b []byte, off int64) (int, error) {
	for {
		if len(me) == 0 {
			return 0, io.EOF
		}
		if me[0].offset <= off {
			break
		}
		me = me[1:]
	}
	n, err := me[0].instance.ReadAt(b, off-me[0].offset)
	if n == len(b) {
		return n, nil
	}
	if err == nil || err == io.EOF {
		n_, err := me[1:].ReadAt(b[n:], off+int64(n))
		return n + n_, err
	}
	return n, err
}

func (s piecePerResourcePiece) getChunks() (chunks chunks) {
	names, err := s.incompleteDir().Readdirnames()
	if err != nil {
		return
	}
	for _, n := range names {
		offset, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			panic(err)
		}
		i, err := s.rp.NewInstance(path.Join(s.incompleteDirPath(), n))
		if err != nil {
			panic(err)
		}
		chunks = append(chunks, chunk{offset, i})
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].offset < chunks[j].offset
	})
	return
}

func (s piecePerResourcePiece) completedInstancePath() string {
	return path.Join("completed", s.mp.Hash().HexString())
}

func (s piecePerResourcePiece) completed() resource.Instance {
	i, err := s.rp.NewInstance(s.completedInstancePath())
	if err != nil {
		panic(err)
	}
	return i
}

func (s piecePerResourcePiece) incompleteDirPath() string {
	return path.Join("incompleted", s.mp.Hash().HexString())
}

func (s piecePerResourcePiece) incompleteDir() resource.DirInstance {
	i, err := s.rp.NewInstance(s.incompleteDirPath())
	if err != nil {
		panic(err)
	}
	return i.(resource.DirInstance)
}
