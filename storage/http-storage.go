// based on anacrolix-torrent/storage/file-client.go

package storage

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"time"

	"github.com/anacrolix/log"

	// "github.com/anacrolix/torrent" // FIXME cycle
	"github.com/anacrolix/torrent/metainfo"
)

// HTTP-based storage for torrents, that isn't yet bound to a particular torrent.
type HttpStorageImpl struct {
	Opts HttpStorageOpts

	FileStorage ClientImplCloser

	HttpClient *http.Client

	HttpContext context.Context

	HttpContextCancel context.CancelFunc

	// torrentClient *torrent.Client // FIXME cycle
}

// HTTP config inherited from torrent.ClientConfig
type InheritedHttpConfig struct {
	// Defines proxy for HTTP requests, such as for trackers.
	// It's commonly set from the result of "net/http".ProxyURL(HTTPProxy).
	HTTPProxy func(*http.Request) (*url.URL, error)
	// Defines DialContext func to use for HTTP requests,
	// such as for fetching metainfo and webtorrent seeds
	HTTPDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// HTTPUserAgent changes default UserAgent for HTTP requests
	HTTPUserAgent string
	// HttpRequestDirector modifies the request before it's sent.
	// Useful for adding authentication headers, for example
	HttpRequestDirector func(*http.Request) error
}

// TODO rename to HttpStorageConfig
type HttpStorageOpts struct {
	InheritedHttpConfig

	HTTPTimeoutSeconds int64

	MetadataURL string // HTTP directory with .torrent files

	ContentURL string // backend for pieces

	MetadataFilesUpdateInterval int64

	// torrentClient *torrent.Client // cycle

	Logger *slog.Logger

	MetadataCacheDir string // local cache directory for .torrent files

	PieceCacheDir string // local cache directory for content piece files

	// TODO remove?
	// no. duplication is bad
	// ContentFileStorageOpts NewFileClientOpts
	ContentFileStorage ClientImplCloser
	// config fields in ContentFileStorage:
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
	// TODO remove? use opts.ContentFileStorage
	fileStorage := opts.ContentFileStorage
	httpClient := http.DefaultClient
	// FIXME retry requests on timeout
	httpTimeout := time.Duration(opts.HTTPTimeoutSeconds) * time.Second
	httpContext, httpContextCancel := context.WithTimeout(context.Background(), httpTimeout)
	httpStorage := &HttpStorageImpl{opts, fileStorage, httpClient, httpContext, httpContextCancel}
	return httpStorage
}

// func (httpStorage HttpStorageImpl) SetTorrentClient(torrentClient *torrent.Client) {
// 	httpStorage.torrentClient = torrentClient
// }

// fix dependency cycle with torrent.ClientConfig
// this must be called after creating the torrent client
func (httpStorage *HttpStorageImpl) InheritTorrentClientConfig(cfg any) error {
	// opts := httpStorage.Opts // copy
	opts := &httpStorage.Opts // reference
	cfgValue := reflect.ValueOf(cfg)
	if cfgValue.Kind() == reflect.Ptr {
		cfgValue = cfgValue.Elem()
	}
	// Inherit HTTPProxy func
	if opts.HTTPProxy == nil {
		f := cfgValue.FieldByName("HTTPProxy")
		if f.IsValid() && f.Kind() == reflect.Func && !f.IsNil() {
			// Extract the function value as an interface{}
			_func, ok := f.Interface().(func(*http.Request) (*url.URL, error))
			if ok {
				opts.HTTPProxy = _func
			}
		}
	}
	// Inherit HTTPDialContext func
	if opts.HTTPDialContext == nil {
		f := cfgValue.FieldByName("HTTPDialContext")
		if f.IsValid() && f.Kind() == reflect.Func && !f.IsNil() {
			// Extract the function value as an interface{}
			_func, ok := f.Interface().(func(ctx context.Context, network, addr string) (net.Conn, error))
			if ok {
				opts.HTTPDialContext = _func
			}
		}
	}
	// Inherit HTTPUserAgent string
	if opts.HTTPUserAgent == "" {
		f := cfgValue.FieldByName("HTTPUserAgent")
		if f.IsValid() && f.Kind() == reflect.String {
			opts.HTTPUserAgent = f.String()
		}
	}
	// Inherit HttpRequestDirector func
	if opts.HttpRequestDirector == nil {
		f := cfgValue.FieldByName("HttpRequestDirector")
		if f.IsValid() && f.Kind() == reflect.Func && !f.IsNil() {
			// Extract the function value as an interface{}
			_func, ok := f.Interface().(func(*http.Request) error)
			if ok {
				opts.HttpRequestDirector = _func
			}
		}
	}
	return nil
}

func (httpStorage *HttpStorageImpl) Close() error {
	httpStorage.HttpContextCancel()
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
