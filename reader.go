package torrent

import (
	"errors"
	"io"
	"os"
)

// Accesses torrent data via a client.
type Reader struct {
	t          *Torrent
	pos        int64
	responsive bool
	readahead  int64
}

var _ io.ReadCloser = &Reader{}

// Don't wait for pieces to complete and be verified. Read calls return as
// soon as they can when the underlying chunks become available.
func (r *Reader) SetResponsive() {
	r.responsive = true
}

func (r *Reader) SetReadahead(readahead int64) {
	r.readahead = readahead
}

func (r *Reader) raisePriorities(off int64, n int) {
	if r.responsive {
		r.t.cl.addUrgentRequests(r.t.torrent, off, n)
	}
	r.t.cl.readRaisePiecePriorities(r.t.torrent, off, int64(n)+r.readahead)
}

func (r *Reader) readable(off int64) (ret bool) {
	// log.Println("readable", off)
	// defer func() {
	// 	log.Println("readable", ret)
	// }()
	req, ok := r.t.offsetRequest(off)
	if !ok {
		panic(off)
	}
	if r.responsive {
		return r.t.haveChunk(req)
	}
	return r.t.pieceComplete(int(req.Index))
}

// How many bytes are available to read. Max is the most we could require.
func (r *Reader) available(off, max int64) (ret int64) {
	for max > 0 {
		req, ok := r.t.offsetRequest(off)
		if !ok {
			break
		}
		if !r.t.haveChunk(req) {
			break
		}
		len1 := int64(req.Length) - (off - r.t.requestOffset(req))
		max -= len1
		ret += len1
		off += len1
	}
	return
}

func (r *Reader) waitReadable(off int64) {
	r.t.Pieces[off/int64(r.t.usualPieceSize())].Event.Wait()
}

func (r *Reader) ReadAt(b []byte, off int64) (n int, err error) {
	return r.readAt(b, off)
}

func (r *Reader) Read(b []byte) (n int, err error) {
	n, err = r.readAt(b, r.pos)
	r.pos += int64(n)
	if n != 0 && err == io.ErrUnexpectedEOF {
		err = nil
	}
	return
}

func (r *Reader) readAt(b []byte, pos int64) (n int, err error) {
	// defer func() {
	// 	log.Println(pos, n, err)
	// }()
	r.t.cl.mu.Lock()
	defer r.t.cl.mu.Unlock()
	maxLen := r.t.Info.TotalLength() - pos
	if maxLen <= 0 {
		err = io.EOF
		return
	}
	if int64(len(b)) > maxLen {
		b = b[:maxLen]
	}
	r.raisePriorities(pos, len(b))
	for !r.readable(pos) {
		r.raisePriorities(pos, len(b))
		r.waitReadable(pos)
	}
	avail := r.available(pos, int64(len(b)))
	// log.Println("available", avail)
	if int64(len(b)) > avail {
		b = b[:avail]
	}
	n, err = dataReadAt(r.t.data, b, pos)
	return
}

func (r *Reader) Close() error {
	r.t = nil
	return nil
}

func (r *Reader) Seek(off int64, whence int) (ret int64, err error) {
	switch whence {
	case os.SEEK_SET:
		r.pos = off
	case os.SEEK_CUR:
		r.pos += off
	case os.SEEK_END:
		r.pos = r.t.Info.TotalLength() + off
	default:
		err = errors.New("bad whence")
	}
	ret = r.pos
	return
}
