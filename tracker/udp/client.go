package udp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

type Client struct {
	connId       ConnectionId
	connIdIssued time.Time
	Dispatcher   *Dispatcher
	Writer       io.Writer
}

func (cl *Client) Announce(
	ctx context.Context, req AnnounceRequest, peers AnnounceResponsePeers, opts Options,
) (
	respHdr AnnounceResponseHeader, err error,
) {
	body, err := marshal(req)
	if err != nil {
		return
	}
	respBody, err := cl.request(ctx, ActionAnnounce, append(body, opts.Encode()...))
	if err != nil {
		return
	}
	r := bytes.NewBuffer(respBody)
	err = Read(r, &respHdr)
	if err != nil {
		err = fmt.Errorf("reading response header: %w", err)
		return
	}
	err = peers.UnmarshalBinary(r.Bytes())
	if err != nil {
		err = fmt.Errorf("reading response peers: %w", err)
	}
	return
}

func (cl *Client) connect(ctx context.Context) (err error) {
	if time.Since(cl.connIdIssued) < time.Minute {
		return nil
	}
	respBody, err := cl.request(ctx, ActionConnect, nil)
	if err != nil {
		return err
	}
	var connResp ConnectionResponse
	err = binary.Read(bytes.NewReader(respBody), binary.BigEndian, &connResp)
	if err != nil {
		return
	}
	cl.connId = connResp.ConnectionId
	cl.connIdIssued = time.Now()
	return
}

func (cl *Client) connIdForRequest(ctx context.Context, action Action) (id ConnectionId, err error) {
	if action == ActionConnect {
		id = ConnectRequestConnectionId
		return
	}
	err = cl.connect(ctx)
	if err != nil {
		return
	}
	id = cl.connId
	return
}

func (cl *Client) requestWriter(ctx context.Context, action Action, body []byte, tId TransactionId) (err error) {
	var buf bytes.Buffer
	for n := 0; ; n++ {
		var connId ConnectionId
		connId, err = cl.connIdForRequest(ctx, action)
		if err != nil {
			return
		}
		buf.Reset()
		err = binary.Write(&buf, binary.BigEndian, RequestHeader{
			ConnectionId:  connId,
			Action:        action,
			TransactionId: tId,
		})
		if err != nil {
			panic(err)
		}
		buf.Write(body)
		_, err = cl.Writer.Write(buf.Bytes())
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout(n)):
		}
	}
}

func (cl *Client) request(ctx context.Context, action Action, body []byte) (respBody []byte, err error) {
	respChan := make(chan DispatchedResponse, 1)
	t := cl.Dispatcher.NewTransaction(func(dr DispatchedResponse) {
		respChan <- dr
	})
	defer t.End()
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- cl.requestWriter(ctx, action, body, t.Id())
	}()
	select {
	case dr := <-respChan:
		if dr.Header.Action == action {
			respBody = dr.Body
		} else if dr.Header.Action == ActionError {
			err = errors.New(string(dr.Body))
		} else {
			err = fmt.Errorf("unexpected response action %v", dr.Header.Action)
		}
	case err = <-writeErr:
		err = fmt.Errorf("write error: %w", err)
	case <-ctx.Done():
		err = ctx.Err()
	}
	return
}
