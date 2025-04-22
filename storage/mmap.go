package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/edsrzf/mmap-go"

	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/mmap_span"
)

type mmapClientImpl struct {
	baseDir string
}

func NewMMap(baseDir string) ClientImpl {
	return &mmapClientImpl{
		baseDir: baseDir,
	}
}

func (s *mmapClientImpl) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (t TorrentImpl, err error) {
	if info == nil {
		panic("can't open a storage for a nil torrent")
	}
	span, err := mMapTorrent(filepath.Join(s.baseDir, infoHash.HexString()), info)
	t = &mmapTorrentStorage{
		info:     info,
		infoHash: infoHash,
		span:     span,
	}
	return
}

func (s *mmapClientImpl) Close() error {
	return nil
}

type mmapTorrentStorage struct {
	infoHash metainfo.Hash
	info     *metainfo.Info
	span     *mmap_span.MMapSpan
}

// ReadAt implements TorrentImpl.
func (ts *mmapTorrentStorage) ReadAt(p []byte, off int64) (n int, err error) {
	return io.NewSectionReader(ts.span, off, ts.info.OffsetToLength(off)).Read(p)
}

// WriteAt implements TorrentImpl.
func (ts *mmapTorrentStorage) WriteAt(p []byte, off int64) (n int, err error) {
	return io.NewOffsetWriter(ts.span, off).Write(p)
}

func (ts *mmapTorrentStorage) Close() error {
	return ts.span.Close()
}

func mMapTorrent(location string, md *metainfo.Info) (mms *mmap_span.MMapSpan, err error) {
	mms = &mmap_span.MMapSpan{}
	defer func() {
		if err != nil {
			mms.Close()
		}
	}()
	for _, miFile := range md.UpvertedFiles() {
		fileName := filepath.Join(append([]string{location}, miFile.Path...)...)
		var mm mmap.MMap
		mm, err = mmapFile(fileName, miFile.Length)
		if err != nil {
			err = fmt.Errorf("file %q: %s", miFile.DisplayPath(md), err)
			return
		}
		if mm != nil {
			mms.Append(mm)
		}
	}
	return
}

func mmapFile(name string, size int64) (ret mmap.MMap, err error) {
	dir := filepath.Dir(name)
	err = os.MkdirAll(dir, 0777)
	if err != nil {
		err = fmt.Errorf("making directory %q: %s", dir, err)
		return
	}
	var file *os.File
	file, err = os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return
	}
	defer file.Close()
	var fi os.FileInfo
	fi, err = file.Stat()
	if err != nil {
		return
	}
	if fi.Size() < size {
		// I think this is necessary on HFS+. Maybe Linux will SIGBUS too if
		// you overmap a file but I'm not sure.
		err = file.Truncate(size)
		if err != nil {
			return
		}
	}
	if size == 0 {
		// Can't mmap() regions with length 0.
		return
	}
	intLen := int(size)
	if int64(intLen) != size {
		err = errors.New("size too large for system")
		return
	}
	ret, err = mmap.MapRegion(file, intLen, mmap.RDWR, 0, 0)
	if err != nil {
		err = fmt.Errorf("error mapping region: %s", err)
		return
	}
	if int64(len(ret)) != size {
		panic(len(ret))
	}
	return
}
