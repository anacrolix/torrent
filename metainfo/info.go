package metainfo

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/anacrolix/missinggo/slices"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/langx"
)

type Option func(*Info)

func OptionPieceLength(n int64) Option {
	return func(i *Info) {
		i.PieceLength = n
	}
}

func OptionDisplayName(s string) Option {
	return func(i *Info) {
		i.Name = s
	}
}

func NewFromReader(src io.Reader, options ...Option) (info *Info, err error) {
	info = langx.Autoptr(langx.Clone(Info{
		PieceLength: bytesx.MiB,
	}, options...))

	digest := md5.New()
	length := readlength(0)
	wrapped := io.TeeReader(src, digest)
	wrapped = io.TeeReader(wrapped, &length)
	if info.Pieces, err = ComputePieces(wrapped, info.PieceLength); err != nil {
		return nil, err
	}

	info.Length = int64(length)
	if info.Name == "" {
		info.Name = hex.EncodeToString(digest.Sum(nil))
	}

	return info, nil
}

func NewInfo(options ...Option) *Info {
	return langx.Autoptr(langx.Clone(Info{
		PieceLength: bytesx.MiB,
	}, options...))
}

func NewFromPath(root string, options ...Option) (info *Info, err error) {
	info = langx.Autoptr(langx.Clone(Info{
		Name:        filepath.Base(root),
		PieceLength: bytesx.MiB,
	}, options...))

	err = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if fi.IsDir() {
			// Directories are implicit in torrent files.
			return nil
		} else if path == root {
			// The root is a file.
			info.Length = fi.Size()
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("error getting relative path: %s", err)
		}
		info.Files = append(info.Files, FileInfo{
			Path:   strings.Split(relPath, string(filepath.Separator)),
			Length: fi.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(info.Files, func(l, r FileInfo) bool {
		return strings.Join(l.Path, "/") < strings.Join(r.Path, "/")
	})

	err = info.GeneratePieces(func(fi FileInfo) (io.ReadCloser, error) {
		return os.Open(filepath.Join(root, strings.Join(fi.Path, string(filepath.Separator))))
	})
	if err != nil {
		return nil, fmt.Errorf("error generating pieces: %s", err)
	}

	return info, err
}

// Compute the pieces from the given reader and block size
func ComputePieces(src io.Reader, length int64) (pieces []byte, err error) {
	if length == 0 {
		return nil, errors.New("piece length must be non-zero")
	}

	for {
		hasher := sha1.New()
		wn, err := io.CopyN(hasher, src, length)
		if err == io.EOF {
			err = nil
		}
		if err != nil {
			return nil, err
		}
		if wn == 0 {
			break
		}
		pieces = hasher.Sum(pieces)
		if wn < length {
			break
		}
	}

	return pieces, nil
}

// Info dictionary.
type Info struct {
	PieceLength  int64      `bencode:"piece length"`
	Pieces       []byte     `bencode:"pieces"`
	Name         string     `bencode:"name"`
	Length       int64      `bencode:"length,omitempty"`
	Private      *bool      `bencode:"private,omitempty"` // pointer to handle backwards compatibility
	Source       string     `bencode:"source,omitempty"`
	Files        []FileInfo `bencode:"files,omitempty"`
	cachedLength int64      // used to cache the total length of the torrent
}

// Concatenates all the files in the torrent into w. open is a function that
// gets at the contents of the given file.
func (info *Info) writeFiles(w io.Writer, open func(fi FileInfo) (io.ReadCloser, error)) error {
	for _, fi := range info.UpvertedFiles() {
		r, err := open(fi)
		if err != nil {
			return fmt.Errorf("error opening %v: %s", fi, err)
		}
		wn, err := io.CopyN(w, r, fi.Length)
		r.Close()
		if wn != fi.Length {
			return fmt.Errorf("error copying %v: %s", fi, err)
		}
	}
	return nil
}

// Sets Pieces (the block of piece hashes in the Info) by using the passed
// function to get at the torrent data.
func (info *Info) GeneratePieces(open func(fi FileInfo) (io.ReadCloser, error)) (err error) {
	pr, pw := io.Pipe()
	go func() {
		err := info.writeFiles(pw, open)
		pw.CloseWithError(err)
	}()
	defer pr.Close()
	info.Pieces, err = ComputePieces(pr, info.PieceLength)
	return err
}

func (info *Info) TotalLength() (ret int64) {
	if cached := atomic.LoadInt64(&info.cachedLength); cached > 0 {
		return cached
	}
	defer atomic.StoreInt64(&info.cachedLength, ret)

	if !info.IsDir() {
		return info.Length
	}

	for _, fi := range info.Files {
		ret += fi.Length
	}

	return ret
}

func (info *Info) NumPieces() int {
	return len(info.Pieces) / 20
}

func (info *Info) IsDir() bool {
	return len(info.Files) != 0
}

// The files field, converted up from the old single-file in the parent info
// dict if necessary. This is a helper to avoid having to conditionally handle
// single and multi-file torrent infos.
func (info *Info) UpvertedFiles() []FileInfo {
	if len(info.Files) == 0 {
		return []FileInfo{{
			Length: info.Length,
			// Callers should determine that Info.Name is the basename, and
			// thus a regular file.
			Path: nil,
		}}
	}
	return info.Files
}

func (info *Info) Piece(index int) Piece {
	return Piece{info, pieceIndex(index)}
}

func (info *Info) OffsetToIndex(offset int64) int64 {
	if info.PieceLength == 0 {
		return 0
	}

	return offset / info.PieceLength
}

func (info *Info) OffsetToLength(offset int64) (length int64) {
	if info.PieceLength == 0 {
		return 0
	}

	index := offset / info.PieceLength
	if index == int64(info.NumPieces()) {
		return info.TotalLength() % info.PieceLength
	}

	return info.PieceLength
}

func (info *Info) Hashes() (ret [][]byte) {
	for i := 0; i < len(info.Pieces); i += sha1.Size {
		ret = append(ret, info.Pieces[i:i+sha1.Size])
	}

	return ret
}

type readlength uint64

func (t *readlength) Write(b []byte) (int, error) {
	bn := len(b)
	atomic.AddUint64((*uint64)(t), uint64(bn))
	return bn, nil
}
