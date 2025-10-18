package storage

import (
	"fmt"
	"io"
	_log "log"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/anacrolix/torrent/segments"
)

// Exposes file-based storage of a torrent, as one big ReadWriterAt.
type HttpTorrentImplIO struct {
	httpTorrent *HttpTorrentImpl
}

/*
// Returns EOF on short or missing file.
func (tio HttpTorrentImplIO) readFileAt(file file, b []byte, off int64) (n int, err error) {
	// tio.httpTorrent.logger().Debug("readFileAt", "file.safeOsPath", file.safeOsPath) // FIXME panic
	tio.httpTorrent.logger().Debug("readFileAt")
	_log.Printf("HttpTorrentImplIO.readFileAt: file=%q off=%d len(b)=%d\n", file.safeOsPath, off, len(b))
	f, err := tio.httpTorrent.openSharedFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		// File missing is treated the same as a short file. Should we propagate this through the
		// interface now that fs.ErrNotExist is a thing?
		err = io.EOF
		return
	}
	if err != nil {
		return
	}
	defer f.Close()
	// Limit the read to within the expected bounds of this file.
	if int64(len(b)) > file.length()-off {
		b = b[:file.length()-off]
	}
	for off < file.length() && len(b) != 0 {
		n1, err1 := f.ReadAt(b, off)
		b = b[n1:]
		n += n1
		off += int64(n1)
		if n1 == 0 {
			err = err1
			break
		}
	}
	return
}
*/

func (tio HttpTorrentImplIO) ReadAt(buf []byte, off int64) (num int, err error) {
	info := tio.httpTorrent.info
	if info == nil {
		return 0, fmt.Errorf("torrent info not yet available")
	}
	storage := tio.httpTorrent.GetStorage()
	btih := tio.httpTorrent.infoHash.String()

	pieceLength := info.PieceLength
	totalLength := info.TotalLength()
	if off >= totalLength {
		return 0, io.EOF
	}

	pieceIdx := int(off / pieceLength)
	pieceOffset := off % pieceLength
	readRemaining := len(buf)

	_log.Printf("HttpTorrentImplIO.ReadAt: btih=%s pieceIdx=%d pieceOffset=%d len=%d\n",
		btih, pieceIdx, pieceOffset, readRemaining)

	// cachePiecePath := filepath.Join(storage.Opts.PieceCacheDir, btih, fmt.Sprintf("%d.piece", pieceIdx))
	cachePiecePath := filepath.Join(storage.Opts.PieceCacheDir, btih, fmt.Sprintf("%d", pieceIdx))
	os.MkdirAll(filepath.Dir(cachePiecePath), 0o755)

	// --- Try reading from local cache ---
	cached, err := os.Open(cachePiecePath)
	if err == nil {
		defer cached.Close()
		_log.Printf("cache hit: %s\n", cachePiecePath)
		num, err = cached.ReadAt(buf, pieceOffset)
		if err == io.EOF && num < readRemaining {
			err = io.ErrUnexpectedEOF
		}
		return num, err
	}

	_log.Printf("cache miss: %s\n", cachePiecePath)

	// --- Cache miss: fetch piece data from backend ---
	pieceTmpPath := cachePiecePath + ".tmp"
	os.MkdirAll(filepath.Dir(pieceTmpPath), 0o755)

	tmpFile, err := os.Create(pieceTmpPath)
	if err != nil {
		return 0, fmt.Errorf("creating temp piece cache: %w", err)
	}
	defer tmpFile.Close()

	// The backend serves full torrent content files under:
	//   {ContentURL}/cas/btih/{btih}/{torrent_name}/{file_path}
	// so we reconstruct the request URLs for the *files* that overlap this piece.
	var pieceStart = int64(pieceIdx) * pieceLength
	var pieceEnd = pieceStart + pieceLength
	if pieceEnd > totalLength {
		pieceEnd = totalLength
	}

	// Iterate through torrent files that overlap this piece.
	for _, fi := range info.Files {
		fileStart := fi.TorrentOffset
		fileEnd := fileStart + fi.Length
		if fileEnd <= pieceStart || fileStart >= pieceEnd {
			continue // no overlap
		}

		// Determine range within this file to fetch
		startInFile := max(pieceStart, fileStart) - fileStart
		endInFile := min(pieceEnd, fileEnd) - fileStart
		// length := endInFile - startInFile

		filePath := path.Join(fi.Path...)
		url := fmt.Sprintf("%s/cas/btih/%s/%s/%s",
			storage.Opts.ContentURL,
			btih,
			info.Name,
			filePath)
		_log.Printf("fetching range [%d:%d) from %s\n", startInFile, endInFile, url)

		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startInFile, endInFile-1))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, fmt.Errorf("fetching piece %d: %w", pieceIdx, err)
		}
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return 0, fmt.Errorf("bad response %d fetching %s", resp.StatusCode, url)
		}

		if _, err := io.Copy(tmpFile, resp.Body); err != nil {
			resp.Body.Close()
			return 0, fmt.Errorf("copying piece data: %w", err)
		}
		resp.Body.Close()
	}

	tmpFile.Close()
	if err := os.Rename(pieceTmpPath, cachePiecePath); err != nil {
		return 0, fmt.Errorf("finalizing cached piece: %w", err)
	}

	// --- Now read the requested range from cache ---
	f, err := os.Open(cachePiecePath)
	if err != nil {
		return 0, fmt.Errorf("reopening cached piece: %w", err)
	}
	defer f.Close()
	num, err = f.ReadAt(buf, pieceOffset)
	if err == io.EOF && num < readRemaining {
		err = io.ErrUnexpectedEOF
	}
	return num, err
}

func (tio HttpTorrentImplIO) WriteAt(p []byte, off int64) (n int, err error) {
	for i, e := range tio.httpTorrent.segmentLocater.LocateIter(
		segments.Extent{off, int64(len(p))},
	) {
		var f fileWriter
		f, err = tio.httpTorrent.openForWrite(tio.httpTorrent.file(i))
		if err != nil {
			return
		}
		var n1 int
		n1, err = f.WriteAt(p[:e.Length], e.Start)
		closeErr := f.Close()
		n += n1
		p = p[n1:]
		if err == nil {
			err = closeErr
		}
		if err == nil && int64(n1) != e.Length {
			err = io.ErrShortWrite
		}
		if err != nil {
			return
		}
	}
	return
}
