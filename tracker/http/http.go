package http

import (
	"bytes"
	"context"
	"expvar"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anacrolix/missinggo/httptoo"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/tracker/shared"
	"github.com/anacrolix/torrent/tracker/udp"
	"github.com/anacrolix/torrent/version"
)

var vars = expvar.NewMap("tracker/http")

func setAnnounceParams(_url *url.URL, ar *AnnounceRequest, opts AnnounceOpt) {
	res :=        "key"     + "=" + strconv.FormatInt(int64(ar.Key),10)   +
		"&" + "peer_id" + "=" + url.QueryEscape(string(ar.PeerId[:])) +
		// AFAICT, port is mandatory, and there's no implied port key.
		"&" + "port"      + "=" + strconv.FormatInt(int64(ar.Port), 10) +
		"&" + "uploaded"  + "=" + strconv.FormatInt(ar.Uploaded, 10)    +
		"&" + "downloaded"+ "=" + strconv.FormatInt(ar.Downloaded, 10)  +
		// The AWS S3 tracker returns "400 Bad Request: left(-1) was not in the valid range 0 -
		// 9223372036854775807" if left is out of range, or "500 Internal Server Error: Internal Server
		// Error" if omitted entirely.
		// Here we set left to math.MaxInt64 if it's -1. We do this by clearing the sign bit
		"&" + "left" + "=" + strconv.FormatInt(ar.Left & math.MaxInt64, 10) +

		func() (event string) { 
			if ar.Event != shared.None { 
				event = "&" + "event" + "=" + ar.Event.String()
			}
			return
		}() +

		// http://stackoverflow.com/questions/17418004/why-does-tracker-server-not-understand-my-request-bittorrent-protocol
		"&" + "compact" + "=" + "1" +

		// According to https://wiki.vuze.com/w/Message_Stream_Encryption. TODO:
		// Take EncryptionPolicy or something like it as a parameter.
		"&" + "supportcrypto" + "=" + "1" +

		// Let's try listing them. BEP 3 mentions having an "ip" param, and BEP 7 says we can list
		// addresses for other address-families, although it's not encouraged.
		func() (ipstr string) {
			if opts.ClientIp4 != nil {
				ipv4 := url.QueryEscape(opts.ClientIp4.String())
				ipstr = "&" + "ip4" + "=" + ipv4 +
					"&" + "ip" + "=" + ipv4
			}
			return
		}() +
		func() (ipstr string) {
			if opts.ClientIp6 != nil {
				ipv6 := url.QueryEscape(opts.ClientIp6.String())
				ipstr = "&" + "ipv6" + "=" + ipv6 +
					"&" + "ip" + "=" + ipv6
			}
			return
		}() +

		"&" + "info_hash" + "="  + strings.ReplaceAll(url.QueryEscape(string(ar.InfoHash[:])), "+", "%20") +

		func() (qstr string) {
			if qstr = _url.Query().Encode(); qstr != "" {
				qstr = "&" + qstr
			}
			return
		}() +

		""

	_url.RawQuery = res
}

type AnnounceOpt struct {
	UserAgent  string
	HostHeader string
	ClientIp4  net.IP
	ClientIp6  net.IP
}

type AnnounceRequest = udp.AnnounceRequest

func (cl Client) Announce(ctx context.Context, ar AnnounceRequest, opt AnnounceOpt) (ret AnnounceResponse, err error) {
	_url := httptoo.CopyURL(cl.url_)
	setAnnounceParams(_url, &ar, opt)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, _url.String(), nil)
	userAgent := opt.UserAgent
	if userAgent == "" {
		userAgent = version.DefaultHttpUserAgent
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Host = opt.HostHeader
	resp, err := cl.hc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	io.Copy(&buf, resp.Body)
	if resp.StatusCode != 200 {
		err = fmt.Errorf("response from tracker: %s: %s", resp.Status, buf.String())
		return
	}
	var trackerResponse HttpResponse
	err = bencode.Unmarshal(buf.Bytes(), &trackerResponse)
	if _, ok := err.(bencode.ErrUnusedTrailingBytes); ok {
		err = nil
	} else if err != nil {
		err = fmt.Errorf("error decoding %q: %s", buf.Bytes(), err)
		return
	}
	if trackerResponse.FailureReason != "" {
		err = fmt.Errorf("tracker gave failure reason: %q", trackerResponse.FailureReason)
		return
	}
	vars.Add("successful http announces", 1)
	ret.Interval = trackerResponse.Interval
	ret.Leechers = trackerResponse.Incomplete
	ret.Seeders = trackerResponse.Complete
	if len(trackerResponse.Peers) != 0 {
		vars.Add("http responses with nonempty peers key", 1)
	}
	ret.Peers = trackerResponse.Peers
	if len(trackerResponse.Peers6) != 0 {
		vars.Add("http responses with nonempty peers6 key", 1)
	}
	for _, na := range trackerResponse.Peers6 {
		ret.Peers = append(ret.Peers, Peer{
			IP:   na.IP,
			Port: na.Port,
		})
	}
	return
}

type AnnounceResponse struct {
	Interval int32 // Minimum seconds the local peer should wait before next announce.
	Leechers int32
	Seeders  int32
	Peers    []Peer
}
