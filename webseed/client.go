package webseed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/RoaringBitmap/roaring"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/storage"
)

type RequestSpec = segments.Extent

type requestPart struct {
	req *http.Request
	e   segments.Extent
	do  func() (*http.Response, io.ReadWriteCloser, error)
	// Wrap http response bodies for such things as download rate limiting.
	responseBodyWrapper ResponseBodyWrapper
	hasher              func(b []byte) uint64 // temp for testing
}

type Request struct {
	cancel  func()
	Result  chan RequestResult
	readers []io.Reader
}

func (r Request) Cancel() {
	r.cancel()
}

type Client struct {
	HttpClient *http.Client
	Url        string
	fileIndex  segments.Index
	info       *metainfo.Info
	// The pieces we can request with the Url. We're more likely to ban/block at the file-level
	// given that's how requests are mapped to webseeds, but the torrent.Client works at the piece
	// level. We can map our file-level adjustments to the pieces here. This probably need to be
	// private in the future, if Client ever starts removing pieces.
	Pieces              roaring.Bitmap
	ResponseBodyWrapper ResponseBodyWrapper
	PathEscaper         PathEscaper
}

type ResponseBodyWrapper func(io.Reader) io.Reader

func (me *Client) SetInfo(info *metainfo.Info) {
	if !strings.HasSuffix(me.Url, "/") && info.IsDir() {
		// In my experience, this is a non-conforming webseed. For example the
		// http://ia600500.us.archive.org/1/items URLs in archive.org torrents.
		return
	}
	me.fileIndex = segments.NewIndex(common.LengthIterFromUpvertedFiles(info.UpvertedFiles()))
	me.info = info
	me.Pieces.AddRange(0, uint64(info.NumPieces()))
}

type RequestResult struct {
	Ctx     context.Context
	Readers []io.ReadCloser
	Err     error
}

func (ws *Client) NewRequest(r RequestSpec, buffers storage.BufferPool, limiter *rate.Limiter, receivingCounter *atomic.Int64, hasher func(b []byte) uint64) Request {
	ctx, cancel := context.WithCancel(context.Background())
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
		part.do = func() (*http.Response, io.ReadWriteCloser, error) {
			buff, err := buffers.Get(ctx, e.Length)

			if err != nil {
				return nil, nil, err
			}

			if err := limiter.WaitN(ctx, int(r.Length)); err != nil {
				buff.Close()
				return nil, nil, err
			}

			response, err := ws.HttpClient.Do(req)

			if err != nil {
				buff.Close()
				return nil, nil, err
			}

			return response, buff, err
		}
		part.hasher = hasher
		requestParts = append(requestParts, part)
		return true
	}) {
		panic("request out of file bounds")
	}
	req := Request{
		cancel: cancel,
		Result: make(chan RequestResult, 1),
	}
	go func() {
		readers, err := readRequestPartResponses(ctx, requestParts, receivingCounter)

		if err != nil {
			for _, reader := range readers {
				reader.Close()
			}

			readers = nil
		}

		req.Result <- RequestResult{
			Readers: readers,
			Err:     err,
			Ctx:     ctx,
		}
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

func recvPartResult(ctx context.Context, writer io.Writer, part requestPart, resp *http.Response) error {
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
		buf, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		fmt.Println("RECV", part.req.URL, part.e.Start/part.e.Length, len(buf), part.hasher(buf))
		copied, err := io.Copy(writer, bytes.NewBuffer(buf))
		if err != nil {
			return err
		}

		if copied != part.e.Length {
			return fmt.Errorf("got %v bytes, expected %v", copied, part.e.Length)
		}
		return nil
	case http.StatusOK:
		// This number is based on
		// https://archive.org/download/BloodyPitOfHorror/BloodyPitOfHorror.asr.srt. It seems that
		// archive.org might be using a webserver implementation that refuses to do partial
		// responses to small files.
		if part.e.Start < 48<<10 {
			if part.e.Start != 0 {
				log.Printf("resp status ok but requested range [url=%q, range=%q]",
					part.req.URL,
					part.req.Header.Get("Range"))
			}
			// Instead of discarding, we could try receiving all the chunks present in the response
			// body. I don't know how one would handle multiple chunk requests resulting in an OK
			// response for the same file. The request algorithm might be need to be smarter for
			// that.
			discarded, _ := io.CopyN(io.Discard, body, part.e.Start)
			if discarded != 0 {
				log.Printf("discarded %v bytes in webseed request response part", discarded)
			}
			_, err := io.CopyN(writer, body, part.e.Length)
			return err
		} else {
			return ErrBadResponse{"resp status ok but requested range", resp}
		}
	case http.StatusServiceUnavailable:
		return ErrTooFast
	default:
		return ErrBadResponse{
			fmt.Sprintf("unhandled response status code (%v)", resp.StatusCode),
			resp,
		}
	}
}

var ErrTooFast = errors.New("making requests too fast")

func readRequestPartResponses(ctx context.Context, parts []requestPart, receivingCounter *atomic.Int64) ([]io.ReadCloser, error) {
	var readers []io.ReadCloser

	for _, part := range parts {
		if result, readWriter, err := part.do(); err != nil {
			return readers, err
		} else {
			if err = func() error {
				receivingCounter.Add(1)
				defer receivingCounter.Add(-1)
				if err := recvPartResult(ctx, readWriter, part, result); err != nil {
					readWriter.Close()
					return err
				}
				readers = append(readers, readWriter)
				return nil
			}(); err != nil {
				return readers, fmt.Errorf("reading %q at %q: %w", part.req.URL, part.req.Header.Get("Range"), err)
			}
		}
	}

	return readers, nil
}
