// based on anacrolix-torrent/storage/file-client.go

package storage

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/anacrolix/log"

	// "github.com/anacrolix/torrent" // FIXME cycle
	"github.com/anacrolix/torrent/metainfo"
)

// HTTP-based storage for torrents, that isn't yet bound to a particular torrent.
type HttpStorageImpl struct {
	Opts        HttpStorageOpts
	FileStorage ClientImplCloser
	// torrentClient *torrent.Client // FIXME cycle
}

type HttpStorageOpts struct {
	MetadataURL string // HTTP directory with .torrent files
	ContentURL  string // backend for pieces
	HTTPClient  *http.Client
	// torrentClient          *torrent.Client
	Logger           *slog.Logger
	MetadataCacheDir string // local cache directory for .torrent files
	PieceCacheDir    string // local cache directory for content piece files
	// no. duplication is bad
	// ContentFileStorageOpts NewFileClientOpts
	ContentFileStorage ClientImplCloser
	// // The base directory for all downloads.
	// ClientBaseDir   string
	// FilePathMaker   FilePathMaker
	// TorrentDirMaker TorrentDirFilePathMaker
	// // If part files are enabled, this will default to inferring completion from file names at
	// // startup, and keep the rest in memory.
	// PieceCompletion PieceCompletion
	// UsePartFiles    g.Option[bool]
	// Logger          *slog.Logger
}

// The specific part-files option or the default.
func (opts HttpStorageOpts) partFiles() bool {
	// panics if ContentFileStorage has wrong type
	fileStorage := (opts.ContentFileStorage).(*FileClientImpl)
	return fileStorage.GetOpts().UsePartFiles.UnwrapOr(true)
}

// NewHttpStorage creates a new ClientImplCloser that stores files using the OS native filesystem.
func NewHttpStorage(opts HttpStorageOpts) ClientImplCloser {
	// fileStorage := NewFileOpts(opts.ContentFileStorageOpts)
	fileStorage := opts.ContentFileStorage
	httpStorage := &HttpStorageImpl{opts, fileStorage}
	return httpStorage
}

// func (httpStorage HttpStorageImpl) SetTorrentClient(torrentClient *torrent.Client) {
// 	httpStorage.torrentClient = torrentClient
// }

func (httpStorage *HttpStorageImpl) Close() error {
	// panics if ContentFileStorage has wrong type
	fileStorage := (httpStorage.Opts.ContentFileStorage).(*FileClientImpl)
	return fileStorage.GetOpts().PieceCompletion.Close()
}

func (httpStorage *HttpStorageImpl) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (_ TorrentImpl, err error) {
	// panics if ContentFileStorage has wrong type
	fileStorage := (httpStorage.Opts.ContentFileStorage).(*FileClientImpl)
	dir := fileStorage.GetOpts().TorrentDirMaker(
		fileStorage.GetOpts().ClientBaseDir, info, infoHash,
	)
	logger := log.ContextLogger(ctx).Slogger()
	logger.DebugContext(ctx, "opened content file storage", slog.String("dir", dir))
	metainfoFileInfos := info.UpvertedFiles()
	files := make([]fileExtra, len(metainfoFileInfos))
	for i, fileInfo := range metainfoFileInfos {
		filePath := filepath.Join(dir, fileStorage.GetOpts().FilePathMaker(FilePathMakerOpts{
			Info: info,
			File: &fileInfo,
		}))
		if !isSubFilepath(dir, filePath) {
			err = fmt.Errorf("file %v: path %q is not sub path of %q", i, filePath, dir)
			return
		}
		files[i].safeOsPath = filePath
		if metainfoFileInfos[i].Length == 0 {
			err = CreateNativeZeroLengthFile(filePath)
			if err != nil {
				err = fmt.Errorf("creating zero length file: %w", err)
				return
			}
		}
	}
	httpTorrent := &HttpTorrentImpl{
		info,
		files,
		metainfoFileInfos,
		info.FileSegmentsIndex(),
		infoHash,
		defaultFileIo(),
		httpStorage,
	}
	if httpTorrent.partFiles() {
		err = httpTorrent.setCompletionFromPartFiles()
		if err != nil {
			err = fmt.Errorf("setting completion from part files: %w", err)
			return
		}
	}
	return TorrentImpl{
		Piece: httpTorrent.Piece,
		Close: httpTorrent.Close,
	}, nil
}
