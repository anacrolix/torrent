package tracker

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	_ "github.com/anacrolix/envpprof"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/tracker/udp"
)

var trackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://localhost:42069",
}

func TestAnnounceLocalhost(t *testing.T) {
	t.Parallel()
	srv := server{
		t: map[[20]byte]torrent{
			{0xa3, 0x56, 0x41, 0x43, 0x74, 0x23, 0xe6, 0x26, 0xd9, 0x38, 0x25, 0x4a, 0x6b, 0x80, 0x49, 0x10, 0xa6, 0x67, 0xa, 0xc1}: {
				Seeders:  1,
				Leechers: 2,
				Peers: krpc.CompactIPv4NodeAddrs{
					{[]byte{1, 2, 3, 4}, 5},
					{[]byte{6, 7, 8, 9}, 10},
				},
			},
		},
	}
	var err error
	srv.pc, err = net.ListenPacket("udp", "localhost:0")
	require.NoError(t, err)
	defer srv.pc.Close()
	go func() {
		require.NoError(t, srv.serveOne())
	}()
	req := AnnounceRequest{
		NumWant: -1,
		Event:   Started,
	}
	rand.Read(req.PeerId[:])
	copy(req.InfoHash[:], []uint8{0xa3, 0x56, 0x41, 0x43, 0x74, 0x23, 0xe6, 0x26, 0xd9, 0x38, 0x25, 0x4a, 0x6b, 0x80, 0x49, 0x10, 0xa6, 0x67, 0xa, 0xc1})
	go func() {
		require.NoError(t, srv.serveOne())
	}()
	ar, err := Announce{
		TrackerUrl: fmt.Sprintf("udp://%s/announce", srv.pc.LocalAddr().String()),
		Request:    req,
	}.Do()
	require.NoError(t, err)
	assert.EqualValues(t, 1, ar.Seeders)
	assert.EqualValues(t, 2, len(ar.Peers))
}

func TestUDPTracker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.SkipNow()
	}
	req := AnnounceRequest{
		NumWant: -1,
	}
	rand.Read(req.PeerId[:])
	copy(req.InfoHash[:], []uint8{0xa3, 0x56, 0x41, 0x43, 0x74, 0x23, 0xe6, 0x26, 0xd9, 0x38, 0x25, 0x4a, 0x6b, 0x80, 0x49, 0x10, 0xa6, 0x67, 0xa, 0xc1})
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTrackerAnnounceTimeout)
	defer cancel()
	if dl, ok := t.Deadline(); ok {
		var cancel func()
		ctx, cancel = context.WithDeadline(context.Background(), dl.Add(-time.Second))
		defer cancel()
	}
	ar, err := Announce{
		TrackerUrl: trackers[0],
		Request:    req,
		Context:    ctx,
	}.Do()
	// Skip any net errors as we don't control the server.
	var ne net.Error
	if errors.As(err, &ne) {
		t.Skip(err)
	}
	require.NoError(t, err)
	t.Logf("%+v", ar)
}

func TestAnnounceRandomInfoHashThirdParty(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		// This test involves contacting third party servers that may have
		// unpredictable results.
		t.SkipNow()
	}
	req := AnnounceRequest{
		Event: Stopped,
	}
	rand.Read(req.PeerId[:])
	rand.Read(req.InfoHash[:])
	wg := sync.WaitGroup{}
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTrackerAnnounceTimeout)
	defer cancel()
	if dl, ok := t.Deadline(); ok {
		var cancel func()
		ctx, cancel = context.WithDeadline(ctx, dl.Add(-time.Second))
		defer cancel()
	}
	for _, url := range trackers {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			resp, err := Announce{
				TrackerUrl: url,
				Request:    req,
				Context:    ctx,
			}.Do()
			if err != nil {
				t.Logf("error announcing to %s: %s", url, err)
				return
			}
			if resp.Leechers != 0 || resp.Seeders != 0 || len(resp.Peers) != 0 {
				// The info hash we generated was random in 2^160 space. If we
				// get a hit, something is weird.
				t.Fatal(resp)
			}
			t.Logf("announced to %s", url)
			cancel()
		}(url)
	}
	wg.Wait()
	cancel()
}

// Check that URLPath option is done correctly.
func TestURLPathOption(t *testing.T) {
	conn, err := net.ListenPacket("udp", "localhost:0")
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	announceErr := make(chan error)
	go func() {
		_, err := Announce{
			TrackerUrl: (&url.URL{
				Scheme: "udp",
				Host:   conn.LocalAddr().String(),
				Path:   "/announce",
			}).String(),
		}.Do()
		defer conn.Close()
		announceErr <- err
	}()
	var b [512]byte
	// conn.SetReadDeadline(time.Now().Add(time.Second))
	_, addr, _ := conn.ReadFrom(b[:])
	r := bytes.NewReader(b[:])
	var h udp.RequestHeader
	udp.Read(r, &h)
	w := &bytes.Buffer{}
	udp.Write(w, udp.ResponseHeader{
		Action:        udp.ActionConnect,
		TransactionId: h.TransactionId,
	})
	udp.Write(w, udp.ConnectionResponse{42})
	conn.WriteTo(w.Bytes(), addr)
	n, _, _ := conn.ReadFrom(b[:])
	r = bytes.NewReader(b[:n])
	udp.Read(r, &h)
	udp.Read(r, &AnnounceRequest{})
	all, _ := io.ReadAll(r)
	if string(all) != "\x02\x09/announce" {
		t.FailNow()
	}
	w = &bytes.Buffer{}
	udp.Write(w, udp.ResponseHeader{
		Action:        udp.ActionAnnounce,
		TransactionId: h.TransactionId,
	})
	udp.Write(w, udp.AnnounceResponseHeader{})
	conn.WriteTo(w.Bytes(), addr)
	require.NoError(t, <-announceErr)
}
