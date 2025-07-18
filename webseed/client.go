package webseed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/dustin/go-humanize"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

// How many consecutive bytes to allow discarding from responses. This number is based on
// https://archive.org/download/BloodyPitOfHorror/BloodyPitOfHorror.asr.srt. It seems that
// archive.org might be using a webserver implementation that refuses to do partial responses to
// small files.
const MaxDiscardBytes = 48 << 10

// Output debug information to stdout.
var PrintDebug = false

func init() {
	_, PrintDebug = os.LookupEnv("TORRENT_WEBSEED_DEBUG")
}

type RequestSpec = segments.Extent

type requestPart struct {
	req *http.Request
	e   segments.Extent
	do  func() (*http.Response, error)
	// Wrap http response bodies for such things as download rate limiting.
	responseBodyWrapper ResponseBodyWrapper
}

type Request struct {
	cancel func()
	Body   io.Reader
	// Closed with error to unstick copy routine when context isn't checked.
	bodyPipe *io.PipeReader
}

func (r Request) Cancel() {
	r.cancel()
}

func (r Request) Close() {
	// We aren't cancelling because we want to know if we can keep receiving buffered data after
	// cancellation. PipeReader.Close always returns nil.
	_ = r.bodyPipe.Close()
}

type Client struct {
	Logger     *slog.Logger
	HttpClient *http.Client
	Url        string
	// Max concurrent requests to a WebSeed for a given torrent.
	MaxRequests int

	fileIndex *segments.Index
	info      *metainfo.Info
	// The pieces we can request with the Url. We're more likely to ban/block at the file-level
	// given that's how requests are mapped to webseeds, but the torrent.Client works at the piece
	// level. We can map our file-level adjustments to the pieces here. This probably need to be
	// private in the future, if Client ever starts removing pieces. TODO: This belongs in
	// webseedPeer.
	Pieces roaring.Bitmap
	// This wraps http.Response bodies, for example to limit the download rate.
	ResponseBodyWrapper     ResponseBodyWrapper
	ResponseBodyRateLimiter *rate.Limiter
	PathEscaper             PathEscaper
}

type ResponseBodyWrapper func(io.Reader) io.Reader

func (me *Client) SetInfo(info *metainfo.Info, fileIndex *segments.Index) {
	if !strings.HasSuffix(me.Url, "/") && info.IsDir() {
		// In my experience, this is a non-conforming webseed. For example the
		// http://ia600500.us.archive.org/1/items URLs in archive.org torrents.
		return
	}
	me.fileIndex = fileIndex
	me.info = info
	me.Pieces.AddRange(0, uint64(info.NumPieces()))
}

type RequestResult struct {
	Bytes []byte
	Err   error
}

// Returns the URL for the given file index. This is assumed to be globally unique.
func (ws *Client) UrlForFileIndex(fileIndex int) string {
	return urlForFileIndex(ws.Url, fileIndex, ws.info, ws.PathEscaper)
}

func (ws *Client) StartNewRequest(r RequestSpec) Request {
	ctx, cancel := context.WithCancel(context.TODO())
	var requestParts []requestPart
	if !ws.fileIndex.Locate(r, func(i int, e segments.Extent) bool {
		req, err := newRequest(
			ctx,
			ws.Url, i, ws.info, e.Start, e.Length,
			ws.PathEscaper,
		)
		if err != nil {
			panic(err)
		}
		part := requestPart{
			req:                 req,
			e:                   e,
			responseBodyWrapper: ws.ResponseBodyWrapper,
		}
		part.do = func() (*http.Response, error) {
			if PrintDebug {
				fmt.Printf(
					"doing request for %q (file size %v), Range: %q\n",
					req.URL,
					humanize.Bytes(uint64(ws.fileIndex.Index(i).Length)),
					req.Header.Get("Range"),
				)
			}
			return ws.HttpClient.Do(req)
		}
		requestParts = append(requestParts, part)
		return true
	}) {
		panic("request out of file bounds")
	}
	body, w := io.Pipe()
	req := Request{
		cancel:   cancel,
		Body:     body,
		bodyPipe: body,
	}
	go func() {
		err := ws.readRequestPartResponses(ctx, w, requestParts)
		panicif.Err(w.CloseWithError(err))
	}()
	return req
}

type ErrBadResponse struct {
	Msg      string
	Response *http.Response
}

func (me ErrBadResponse) Error() string {
	return me.Msg
}

// Warn about bad content-lengths.
func (me *Client) checkContentLength(resp *http.Response, part requestPart, expectedLen int64) {
	if resp.ContentLength == -1 {
		return
	}
	switch resp.Header.Get("Content-Encoding") {
	case "identity", "":
	default:
		return
	}
	if resp.ContentLength != expectedLen {
		me.Logger.Warn("unexpected identity response Content-Length value",
			"actual", resp.ContentLength,
			"expected", expectedLen,
			"url", part.req.URL)
	}
}

// Reads the part in full. All expected bytes must be returned or there will an error returned.
func (me *Client) recvPartResult(ctx context.Context, w io.Writer, part requestPart, resp *http.Response) error {
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if part.responseBodyWrapper != nil {
		body = part.responseBodyWrapper(body)
	}
	// Prevent further accidental use
	resp.Body = nil
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// The response should be just as long as we requested.
		me.checkContentLength(resp, part, part.e.Length)
		copied, err := io.Copy(w, body)
		if err != nil {
			return err
		}
		if copied != part.e.Length {
			return fmt.Errorf("got %v bytes, expected %v", copied, part.e.Length)
		}
		return nil
	case http.StatusOK:
		// The response is from the beginning.
		me.checkContentLength(resp, part, part.e.End())
		discard := part.e.Start
		if discard > MaxDiscardBytes {
			return ErrBadResponse{"resp status ok but requested range", resp}
		}
		if discard != 0 {
			log.Printf("resp status ok but requested range [url=%q, range=%q]",
				part.req.URL,
				part.req.Header.Get("Range"))
		}
		// Instead of discarding, we could try receiving all the chunks present in the response
		// body. I don't know how one would handle multiple chunk requests resulting in an OK
		// response for the same file. The request algorithm might be need to be smarter for that.
		discarded, err := io.CopyN(io.Discard, body, discard)
		if err != nil {
			return fmt.Errorf("error discarding bytes from http ok response: %w", err)
		}
		panicif.NotEq(discarded, discard)
		// Because the reply is not a partial aware response, we limit the body reader
		// intentionally.
		_, err = io.CopyN(w, body, part.e.Length)
		return err
	case http.StatusServiceUnavailable:
		// TODO: Include all of Erigon's cases here?
		return ErrTooFast
	default:
		// TODO: Could we have a slog.Valuer or something to allow callers to unpack reasonable values?
		return ErrBadResponse{
			fmt.Sprintf("unhandled response status code (%v)", resp.Status),
			resp,
		}
	}
}

var ErrTooFast = errors.New("making requests too fast")

func (me *Client) readRequestPartResponses(ctx context.Context, w io.Writer, parts []requestPart) (err error) {
	for _, part := range parts {
		var resp *http.Response
		resp, err = part.do()
		if err == nil {
			err = me.recvPartResult(ctx, w, part, resp)
		}
		if err != nil {
			err = fmt.Errorf("reading %q at %q: %w", part.req.URL, part.req.Header.Get("Range"), err)
			break
		}
	}
	return
}
