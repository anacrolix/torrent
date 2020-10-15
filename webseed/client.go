package webseed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

type RequestSpec = segments.Extent

type requestPartResult struct {
	resp *http.Response
	err  error
}

type requestPart struct {
	req    *http.Request
	e      segments.Extent
	result chan requestPartResult
}

type Request struct {
	cancel func()
	Result chan RequestResult
}

func (r Request) Cancel() {
	r.cancel()
}

type Client struct {
	HttpClient *http.Client
	Url        string
	FileIndex  segments.Index
	Info       *metainfo.Info
}

type RequestResult struct {
	Bytes []byte
	Err   error
}

func (ws *Client) NewRequest(r RequestSpec) Request {
	ctx, cancel := context.WithCancel(context.Background())
	var requestParts []requestPart
	if !ws.FileIndex.Locate(r, func(i int, e segments.Extent) bool {
		req, err := NewRequest(ws.Url, i, ws.Info, e.Start, e.Length)
		if err != nil {
			panic(err)
		}
		req = req.WithContext(ctx)
		part := requestPart{
			req:    req,
			result: make(chan requestPartResult, 1),
			e:      e,
		}
		go func() {
			resp, err := ws.HttpClient.Do(req)
			part.result <- requestPartResult{
				resp: resp,
				err:  err,
			}
		}()
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
		b, err := readRequestPartResponses(requestParts)
		req.Result <- RequestResult{
			Bytes: b,
			Err:   err,
		}
	}()
	return req
}

func recvPartResult(buf io.Writer, part requestPart) error {
	result := <-part.result
	if result.err != nil {
		return result.err
	}
	defer result.resp.Body.Close()
	switch result.resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		if part.e.Start != 0 {
			return errors.New("got status ok but request was at offset")
		}
	default:
		return fmt.Errorf("unhandled response status code (%v)", result.resp.StatusCode)
	}
	copied, err := io.Copy(buf, result.resp.Body)
	if err != nil {
		return err
	}
	if copied != part.e.Length {
		return fmt.Errorf("got %v bytes, expected %v", copied, part.e.Length)
	}
	return nil
}

func readRequestPartResponses(parts []requestPart) ([]byte, error) {
	var buf bytes.Buffer
	for _, part := range parts {
		err := recvPartResult(&buf, part)
		if err != nil {
			return buf.Bytes(), fmt.Errorf("reading %q at %q: %w", part.req.URL, part.req.Header.Get("Range"), err)
		}
	}
	return buf.Bytes(), nil
}
