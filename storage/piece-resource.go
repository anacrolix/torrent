package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/resource"
	"github.com/anacrolix/sync"

	"github.com/anacrolix/torrent/metainfo"
)

type piecePerResource struct {
	rp   PieceProvider
	opts ResourcePiecesOpts
}

type ResourcePiecesOpts struct {
	// After marking a piece complete, don't bother deleting its incomplete blobs.
	LeaveIncompleteChunks bool
	// Sized puts require being able to stream from a statement executed on another connection.
	// Without them, we buffer the entire read and then put that.
	NoSizedPuts bool
	Capacity    TorrentCapacity
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
	locks []sync.RWMutex
}

func (piecePerResourceTorrentImpl) Close() error {
	return nil
}

func (s piecePerResource) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (TorrentImpl, error) {
	t := piecePerResourceTorrentImpl{
		s,
		make([]sync.RWMutex, info.NumPieces()),
	}
	return TorrentImpl{
		PieceWithHash: t.Piece,
		Close:         t.Close,
		Capacity:      s.opts.Capacity,
	}, nil
}

func (s piecePerResourceTorrentImpl) Piece(p metainfo.Piece, pieceHash g.Option[[]byte]) PieceImpl {
	return piecePerResourcePiece{
		mp:               p,
		pieceHash:        pieceHash,
		piecePerResource: s.piecePerResource,
		mu:               &s.locks[p.Index()],
	}
}

type PieceProvider interface {
	resource.Provider
}

type MovePrefixer interface {
	MovePrefix(old, new string) error
}

type ConsecutiveChunkReader interface {
	ReadConsecutiveChunks(prefix string) (io.ReadCloser, error)
}

type PrefixDeleter interface {
	DeletePrefix(prefix string) error
}

type piecePerResourcePiece struct {
	mp metainfo.Piece
	// The piece hash if we have it. It could be 20 or 32 bytes depending on the info version.
	pieceHash g.Option[[]byte]
	piecePerResource
	// This protects operations that move complete/incomplete pieces around, which can trigger read
	// errors that may cause callers to do more drastic things.
	mu *sync.RWMutex
}

var _ io.WriterTo = piecePerResourcePiece{}

func (s piecePerResourcePiece) WriteTo(w io.Writer) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mustIsComplete() {
		if s.hasMovePrefix() {
			if ccr, ok := s.rp.(ConsecutiveChunkReader); ok {
				return s.writeConsecutiveChunks(ccr, s.completedDirPath(), w)
			}
		}
		r, err := s.completedInstance().Get()
		if err != nil {
			return 0, fmt.Errorf("getting complete instance: %w", err)
		}
		defer r.Close()
		return io.Copy(w, r)
	}
	if ccr, ok := s.rp.(ConsecutiveChunkReader); ok {
		return s.writeConsecutiveChunks(ccr, s.incompleteDirPath(), w)
	}
	return io.Copy(w, io.NewSectionReader(s, 0, s.mp.Length()))
}

func (s piecePerResourcePiece) writeConsecutiveChunks(
	ccw ConsecutiveChunkReader,
	dir string,
	w io.Writer,
) (int64, error) {
	r, err := ccw.ReadConsecutiveChunks(dir + "/")
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return io.Copy(w, r)
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

func (s piecePerResourcePiece) Completion() (_ Completion) {
	if !s.pieceHash.Ok {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fi, err := s.completedInstance().Stat()
	if s.hasMovePrefix() {
		return Completion{
			Complete: err == nil && fi.Size() != 0,
			Ok:       true,
		}
	}
	return Completion{
		Complete: err == nil && fi.Size() == s.mp.Length(),
		Ok:       true,
	}
}

type SizedPutter interface {
	PutSized(io.Reader, int64) error
}

func (s piecePerResourcePiece) MarkComplete() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mp, ok := s.rp.(MovePrefixer); ok {
		err = mp.MovePrefix(s.incompleteDirPath()+"/", s.completedDirPath()+"/")
		if err != nil {
			err = fmt.Errorf("moving incomplete to complete: %w", err)
		}
		return
	}
	incompleteChunks := s.getChunks(s.incompleteDirPath())
	r, err := func() (io.ReadCloser, error) {
		if ccr, ok := s.rp.(ConsecutiveChunkReader); ok {
			return ccr.ReadConsecutiveChunks(s.incompleteDirPath() + "/")
		}
		return io.NopCloser(io.NewSectionReader(incompleteChunks, 0, s.mp.Length())), nil
	}()
	if err != nil {
		return fmt.Errorf("getting incomplete chunks reader: %w", err)
	}
	defer r.Close()
	completedInstance := s.completedInstance()
	err = func() error {
		if sp, ok := completedInstance.(SizedPutter); ok && !s.opts.NoSizedPuts {
			return sp.PutSized(r, s.mp.Length())
		} else {
			return completedInstance.Put(r)
		}
	}()
	if err != nil || s.opts.LeaveIncompleteChunks {
		return
	}

	// I think we do this synchronously here since we don't want callers to act on the completed
	// piece if we're concurrently still deleting chunks. The caller may decide to start
	// downloading chunks again and won't expect us to delete them. It seems to be much faster
	// to let the resource provider do this if possible.
	if pd, ok := s.rp.(PrefixDeleter); ok {
		err = pd.DeletePrefix(s.incompleteDirPath() + "/")
		if err != nil {
			err = fmt.Errorf("deleting incomplete prefix: %w", err)
		}
	} else {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completedInstance().Delete()
}

func (s piecePerResourcePiece) ReadAt(b []byte, off int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mustIsComplete() {
		if s.hasMovePrefix() {
			chunks := s.getChunks(s.completedDirPath())
			return chunks.ReadAt(b, off)
		}
		return s.completedInstance().ReadAt(b, off)
	}
	return s.getChunks(s.incompleteDirPath()).ReadAt(b, off)
}

func (s piecePerResourcePiece) WriteAt(b []byte, off int64) (n int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, err := s.rp.NewInstance(path.Join(s.incompleteDirPath(), strconv.FormatInt(off, 10)))
	if err != nil {
		panic(err)
	}
	r := bytes.NewReader(b)
	if sp, ok := i.(SizedPutter); ok {
		err = sp.PutSized(r, r.Size())
	} else {
		err = i.Put(r)
	}
	n = len(b) - r.Len()
	return
}

type chunk struct {
	offset   int64
	instance resource.Instance
}

type chunks []chunk

func (me chunks) ReadAt(b []byte, off int64) (n int, err error) {
	i := sort.Search(len(me), func(i int) bool {
		return me[i].offset > off
	}) - 1
	if i == -1 {
		err = io.EOF
		return
	}
	chunk := me[i]
	// Go made me do this with it's bullshit named return values and := operator.
again:
	n1, err := chunk.instance.ReadAt(b, off-chunk.offset)
	b = b[n1:]
	n += n1
	// Should we check here that we're not io.EOF or nil, per ReadAt's contract? That way we know we
	// don't have an error anymore for the rest of the block.
	if len(b) == 0 {
		// err = nil, so we don't send io.EOF on chunk boundaries?
		return
	}
	off += int64(n1)
	i++
	if i >= len(me) {
		if err == nil {
			err = io.EOF
		}
		return
	}
	chunk = me[i]
	if chunk.offset > off {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return
	}
	goto again
}

func (s piecePerResourcePiece) getChunks(dir string) (chunks chunks) {
	names, err := s.dirInstance(dir).Readdirnames()
	if err != nil {
		return
	}
	for _, n := range names {
		offset, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			panic(err)
		}
		i, err := s.rp.NewInstance(path.Join(dir, n))
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

func (s piecePerResourcePiece) completedDirPath() string {
	if !s.hasMovePrefix() {
		panic("not move prefixing")
	}
	return path.Join("completed", s.hashHex())
}

func (s piecePerResourcePiece) completedInstancePath() string {
	if s.hasMovePrefix() {
		return s.completedDirPath() + "/0"
	}
	return path.Join("completed", s.hashHex())
}

func (s piecePerResourcePiece) completedInstance() resource.Instance {
	i, err := s.rp.NewInstance(s.completedInstancePath())
	if err != nil {
		panic(err)
	}
	return i
}

func (s piecePerResourcePiece) incompleteDirPath() string {
	return path.Join("incompleted", s.hashHex())
}

func (s piecePerResourcePiece) dirInstance(path string) resource.DirInstance {
	i, err := s.rp.NewInstance(path)
	if err != nil {
		panic(err)
	}
	return i.(resource.DirInstance)
}

func (me piecePerResourcePiece) hashHex() string {
	return hex.EncodeToString(me.pieceHash.Unwrap())
}

func (me piecePerResourcePiece) hasMovePrefix() bool {
	_, ok := me.rp.(MovePrefixer)
	return ok
}
