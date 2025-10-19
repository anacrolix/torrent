// based on anacrolix-torrent/storage/file-torrent-io.go

package storage

import (
	"context"
	"encoding/hex"
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
	HttpClient  *http.Client
	HttpContext context.Context
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
	// metainfo.Info // type Info struct
	info := tio.httpTorrent.info
	if info == nil {
		return 0, fmt.Errorf("no torrent info")
	}
	storage := tio.httpTorrent.GetStorage()

	// TODO what is infoHash for hybrid/v2 torrents?
	// does it prefer v2 hash (btmh) like qBittorrent?
	// qBittorrent prefers truncated v2 hash as "torrent ID"
	// https://github.com/qbittorrent/qBittorrent/issues/18185#issuecomment-1345499634
	btih := tio.httpTorrent.infoHash.String()

	pieceLength := info.PieceLength
	totalLength := info.TotalLength()
	if off >= totalLength {
		return 0, io.EOF
	}

	pieceIdx := int(off / pieceLength)
	pieceOffset := off % pieceLength
	readRemaining := len(buf)

	// no: piece.hash undefined (type metainfo.Piece has no field or method hash)
	// piece := info.Piece(pieceIdx)

	// add the piece hash to the piece filename
	// to help other tools verify the cached piece files
	pieceHash := ""
	pieceHashSuffix := ""
	if info.HasV1() {
		// v1 or hybrid torrent
		pieceHash = hex.EncodeToString(info.Pieces[pieceIdx*20 : (pieceIdx+1)*20])
		pieceHashSuffix = "." + pieceHash
	}
	/*
		else {
			// v2 torrent
			// TODO info -> file -> pieces root -> get merkle tree from peers (BEP 30)
			// https://bittorrent.org/beps/bep_0030.html
			// Tr_hashpiece
			// not implemented:
			// https://github.com/anacrolix/torrent/issues/175#issuecomment-1987015148
			// Replying to hash request
			// Asking for proof layers in outbound hash request
			pieceHash = TODO
			pieceHashSuffix = "." + pieceHash
			// type FileTreeFile struct {
			// 	Length     int64  `bencode:"length"`
			// 	PiecesRoot string `bencode:"pieces root"`
			// }
		}
	*/

	_log.Printf("HttpTorrentImplIO.ReadAt: btih=%s pieceIdx=%d pieceOffset=%d len=%d\n",
		btih, pieceIdx, pieceOffset, readRemaining)

	// cachePiecePath := filepath.Join(storage.Opts.PieceCacheDir, btih, fmt.Sprintf("%d.piece", pieceIdx))
	cachePiecePath := filepath.Join(storage.Opts.PieceCacheDir, btih, fmt.Sprintf("%d%s", pieceIdx, pieceHashSuffix))
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
		// cas filesystem https://github.com/milahu/cas-filesystem-spec
		url := fmt.Sprintf("%s/cas/btih/%s/%s/%s",
			storage.Opts.ContentURL,
			btih,
			info.Name,
			filePath)
		_log.Printf("fetching range [%d:%d) from %s\n", startInFile, endInFile, url)

		// based on anacrolix-torrent/tracker/http/http.go
		// req, err := http.NewRequest("GET", url, nil)
		req, err := http.NewRequestWithContext(storage.HttpContext, http.MethodGet, url, nil)
		if err != nil {
			return 0, fmt.Errorf("creating HTTP request: %w", err)
		}

		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startInFile, endInFile-1))

		opt := storage.Opts

		if opt.HTTPUserAgent != "" {
			req.Header.Set("User-Agent", opt.HTTPUserAgent)
		}

		if opt.HttpRequestDirector != nil {
			err = opt.HttpRequestDirector(req)
			if err != nil {
				return 0, fmt.Errorf("modifying HTTP request: %w", err)
			}
		}

		// TODO why set the Host header?
		// req.Host = opt.HostHeader
		// req.Host = "localhost"

		resp, err := storage.HttpClient.Do(req)
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
