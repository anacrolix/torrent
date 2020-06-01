package webseed

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

type RequestSpec = segments.Extent

type httpRequestResult struct {
	resp *http.Response
	err  error
}

type requestPart struct {
	req    *http.Request
	e      segments.Extent
	result chan httpRequestResult
}

type request struct {
	cancel func()
}

type Client struct {
	HttpClient *http.Client
	Url        string
	FileIndex  segments.Index
	Info       *metainfo.Info

	requests map[RequestSpec]request
	Events   chan ClientEvent
}

type ClientEvent struct {
	RequestSpec RequestSpec
	Bytes       []byte
	Err         error
}

func (ws *Client) Cancel(r RequestSpec) {
	ws.requests[r].cancel()
}

func (ws *Client) Request(r RequestSpec) {
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
			result: make(chan httpRequestResult, 1),
			e:      e,
		}
		go func() {
			resp, err := ws.HttpClient.Do(req)
			part.result <- httpRequestResult{
				resp: resp,
				err:  err,
			}
		}()
		requestParts = append(requestParts, part)
		return true
	}) {
		panic("request out of file bounds")
	}
	if ws.requests == nil {
		ws.requests = make(map[RequestSpec]request)
	}
	ws.requests[r] = request{cancel}
	go func() {
		b, err := readRequestPartResponses(requestParts)
		ws.Events <- ClientEvent{
			RequestSpec: r,
			Bytes:       b,
			Err:         err,
		}
	}()
}

func recvPartResult(buf io.Writer, part requestPart) error {
	result := <-part.result
	if result.err != nil {
		return result.err
	}
	defer result.resp.Body.Close()
	if part.e.Start != 0 && result.resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("expected partial content response got %v", result.resp.StatusCode)
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
			return buf.Bytes(), err
		}
	}
	return buf.Bytes(), nil
}
