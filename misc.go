package torrent

import (
	"crypto"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"bitbucket.org/anacrolix/go.torrent/mmap_span"
	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"github.com/anacrolix/libtorgo/metainfo"
	"launchpad.net/gommap"
)

const (
	pieceHash   = crypto.SHA1
	maxRequests = 250        // Maximum pending requests we allow peers to send us.
	chunkSize   = 0x4000     // 16KiB
	BEP20       = "-GT0000-" // Peer ID client identifier prefix
	dialTimeout = time.Second * 15
)

type (
	InfoHash [20]byte
	pieceSum [20]byte
)

type piece struct {
	Hash              pieceSum
	PendingChunkSpecs map[chunkSpec]struct{}
	Hashing           bool
	QueuedForHash     bool
	EverHashed        bool
}

func (p *piece) shuffledPendingChunkSpecs() (css []chunkSpec) {
	if len(p.PendingChunkSpecs) == 0 {
		return
	}
	css = make([]chunkSpec, 0, len(p.PendingChunkSpecs))
	for cs := range p.PendingChunkSpecs {
		css = append(css, cs)
	}
	if len(css) <= 1 {
		return
	}
	for i := range css {
		j := rand.Intn(i + 1)
		css[i], css[j] = css[j], css[i]
	}
	return
}

func (p *piece) Complete() bool {
	return len(p.PendingChunkSpecs) == 0 && p.EverHashed
}

func lastChunkSpec(pieceLength peer_protocol.Integer) (cs chunkSpec) {
	cs.Begin = (pieceLength - 1) / chunkSize * chunkSize
	cs.Length = pieceLength - cs.Begin
	return
}

type chunkSpec struct {
	Begin, Length peer_protocol.Integer
}

type request struct {
	Index peer_protocol.Integer
	chunkSpec
}

func newRequest(index, begin, length peer_protocol.Integer) request {
	return request{index, chunkSpec{begin, length}}
}

type pieceByBytesPendingSlice struct {
	Pending, Indices []peer_protocol.Integer
}

func (pcs pieceByBytesPendingSlice) Len() int {
	return len(pcs.Indices)
}

func (me pieceByBytesPendingSlice) Less(i, j int) bool {
	return me.Pending[me.Indices[i]] < me.Pending[me.Indices[j]]
}

func (me pieceByBytesPendingSlice) Swap(i, j int) {
	me.Indices[i], me.Indices[j] = me.Indices[j], me.Indices[i]
}

var (
	// Requested data not yet available.
	ErrDataNotReady = errors.New("data not ready")
)

func upvertedSingleFileInfoFiles(info *metainfo.Info) []metainfo.FileInfo {
	if len(info.Files) != 0 {
		return info.Files
	}
	return []metainfo.FileInfo{{Length: info.Length, Path: nil}}
}

func mmapTorrentData(md *metainfo.Info, location string) (mms mmap_span.MMapSpan, err error) {
	defer func() {
		if err != nil {
			mms.Close()
			mms = nil
		}
	}()
	for _, miFile := range upvertedSingleFileInfoFiles(md) {
		fileName := filepath.Join(append([]string{location, md.Name}, miFile.Path...)...)
		err = os.MkdirAll(filepath.Dir(fileName), 0777)
		if err != nil {
			return
		}
		var file *os.File
		file, err = os.OpenFile(fileName, os.O_CREATE|os.O_RDWR, 0666)
		if err != nil {
			return
		}
		func() {
			defer file.Close()
			var fi os.FileInfo
			fi, err = file.Stat()
			if err != nil {
				return
			}
			if fi.Size() < miFile.Length {
				err = file.Truncate(miFile.Length)
				if err != nil {
					return
				}
			}
			var mMap gommap.MMap
			mMap, err = gommap.MapRegion(file.Fd(), 0, miFile.Length, gommap.PROT_READ|gommap.PROT_WRITE, gommap.MAP_SHARED)
			if err != nil {
				return
			}
			if int64(len(mMap)) != miFile.Length {
				panic("mmap has wrong length")
			}
			mms = append(mms, mMap)
		}()
		if err != nil {
			return
		}
	}
	return
}

func metadataPieceSize(totalSize int, piece int) int {
	ret := totalSize - piece*(1<<14)
	if ret > 1<<14 {
		ret = 1 << 14
	}
	return ret
}
