package tracker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/james-lawrence/torrent/dht/krpc"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/internal/errorsx"
)

const (
	ErrMissingInfoHash = errorsx.String("missing info hash")
)

type HttpResponse struct {
	FailureReason string `bencode:"failure reason"`
	Warning       string `bencode:"warning message"`
	Interval      int32  `bencode:"interval"`
	TrackerId     string `bencode:"tracker id"`
	Complete      int32  `bencode:"complete"`
	Incomplete    int32  `bencode:"incomplete"`
	Peers         Peers  `bencode:"peers"`
	// BEP 7
	Peers6 krpc.CompactIPv6NodeAddrs `bencode:"peers6"`
}

type Peers []Peer

func (me *Peers) UnmarshalBencode(b []byte) (err error) {
	var _v interface{}
	err = bencode.Unmarshal(b, &_v)
	if err != nil {
		return
	}
	switch v := _v.(type) {
	case string:
		vars.Add("http responses with string peers", 1)
		var cnas krpc.CompactIPv4NodeAddrs
		err = cnas.UnmarshalBinary([]byte(v))
		if err != nil {
			return
		}
		for _, cp := range cnas {
			*me = append(*me, Peer{
				IP:   cp.IP(),
				Port: int(cp.Port()),
			})
		}
		return
	case []interface{}:
		vars.Add("http responses with list peers", 1)
		for _, i := range v {
			var p Peer
			p.FromDictInterface(i.(map[string]interface{}))
			*me = append(*me, p)
		}
		return
	default:
		vars.Add("http responses with unhandled peers type", 1)
		err = fmt.Errorf("unsupported type: %T", _v)
		return
	}
}

func setAnnounceParams(_url *url.URL, ar *AnnounceRequest, opts Announce) {
	q := _url.Query()

	q.Set("info_hash", string(ar.InfoHash[:]))
	q.Set("peer_id", string(ar.PeerId[:]))
	// AFAICT, port is mandatory, and there's no implied port key.
	q.Set("port", fmt.Sprintf("%d", ar.Port))
	q.Set("uploaded", strconv.FormatInt(ar.Uploaded, 10))
	q.Set("downloaded", strconv.FormatInt(ar.Downloaded, 10))

	// The AWS S3 tracker returns "400 Bad Request: left(-1) was not in the valid range 0 -
	// 9223372036854775807" if left is out of range, or "500 Internal Server Error: Internal Server
	// Error" if omitted entirely.
	q.Set("left", strconv.FormatInt(ar.Left, 10))

	if ar.Event != None {
		q.Set("event", ar.Event.String())
	}
	// http://stackoverflow.com/questions/17418004/why-does-tracker-server-not-understand-my-request-bittorrent-protocol
	q.Set("compact", "1")

	// According to https://wiki.vuze.com/w/Message_Stream_Encryption. TODO:
	// Take EncryptionPolicy or something like it as a parameter.
	q.Set("supportcrypto", "1")
	if opts.ClientIp4.Addr().Is4() {
		q.Set("ipv4", opts.ClientIp4.String())
	}
	if opts.ClientIp6.Addr().Is6() {
		q.Set("ipv6", opts.ClientIp6.String())
	}
	_url.RawQuery = q.Encode()
}

func announceHTTP(ctx context.Context, _url *url.URL, ar AnnounceRequest, opt Announce) (ret AnnounceResponse, err error) {
	dup, err := url.Parse(_url.String())
	if err != nil {
		return ret, err
	}

	setAnnounceParams(dup, &ar, opt)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dup.String(), nil)
	req.Header.Set("User-Agent", opt.UserAgent)

	resp, err := (&http.Client{
		Timeout: time.Second * 15,
		Transport: &http.Transport{
			DialContext:         opt.Dialer.DialContext,
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}).Do(req)
	if err != nil {
		return ret, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err = io.Copy(&buf, resp.Body); err != nil {
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, fmt.Errorf("response from tracker: %s: %s", resp.Status, buf.String())
	}
	var trackerResponse HttpResponse
	err = bencode.Unmarshal(buf.Bytes(), &trackerResponse)
	if _, ok := err.(bencode.ErrUnusedTrailingBytes); ok {
		err = nil
	} else if err != nil {
		return ret, fmt.Errorf("error decoding %q: %s", buf.Bytes(), err)
	}

	if trackerResponse.FailureReason != "" {
		switch trackerResponse.FailureReason {
		case "InfoHash not found.", "Torrent has been deleted.":
			return ret, ErrMissingInfoHash
		default:
			return ret, fmt.Errorf("tracker gave failure reason: %q", trackerResponse.FailureReason)
		}
	}

	ret.Interval = trackerResponse.Interval
	ret.Leechers = trackerResponse.Incomplete
	ret.Seeders = trackerResponse.Complete
	ret.Peers = trackerResponse.Peers

	for _, na := range trackerResponse.Peers6 {
		ret.Peers = append(ret.Peers, Peer{
			IP:   na.Addr().AsSlice(),
			Port: int(na.Port()),
		})
	}

	return ret, nil
}
