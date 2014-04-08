package udp_tracker

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"

	"bitbucket.org/anacrolix/go.torrent/tracker"
)

// Ensure net.IPs are stored big-endian, to match the way they're read from
// the wire.
func TestNetIPv4Bytes(t *testing.T) {
	ip := net.IP([]byte{127, 0, 0, 1})
	if ip.String() != "127.0.0.1" {
		t.FailNow()
	}
	if string(ip) != "\x7f\x00\x00\x01" {
		t.Fatal([]byte(ip))
	}
}

func TestMarshalAnnounceResponse(t *testing.T) {
	w := bytes.NewBuffer(nil)
	if err := binary.Write(w, binary.BigEndian, []Peer{{[4]byte{127, 0, 0, 1}, 2}, {[4]byte{255, 0, 0, 3}, 4}}); err != nil {
		t.Fatalf("error writing udp announce response addrs: %s", err)
	}
	if w.String() != "\x7f\x00\x00\x01\x00\x02\xff\x00\x00\x03\x00\x04" {
		t.FailNow()
	}
	if binary.Size(AnnounceResponseHeader{}) != 12 {
		t.FailNow()
	}
}

// Failure to write an entire packet to UDP is expected to given an error.
func TestLongWriteUDP(t *testing.T) {
	l, err := net.ListenUDP("udp", nil)
	defer l.Close()
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialUDP("udp", nil, l.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for msgLen := 1; ; msgLen *= 2 {
		n, err := c.Write(make([]byte, msgLen))
		if err != nil {
			err := err.(*net.OpError).Err
			if err != syscall.EMSGSIZE {
				t.Fatalf("write error isn't EMSGSIZE: %s", err)
			}
			return
		}
		if n < msgLen {
			t.FailNow()
		}
	}
}

func TestShortBinaryRead(t *testing.T) {
	var data ResponseHeader
	err := binary.Read(bytes.NewBufferString("\x00\x00\x00\x01"), binary.BigEndian, &data)
	if data.Action != 0 {
		t.Log("optimistic binary read now works?!")
	}
	switch err {
	case io.ErrUnexpectedEOF:
	default:
		// TODO
	}
}

func TestConvertInt16ToInt(t *testing.T) {
	i := 50000
	if int(uint16(int16(i))) != 50000 {
		t.FailNow()
	}
}

func TestUDPTracker(t *testing.T) {
	tr, err := tracker.New("udp://tracker.openbittorrent.com:80/announce")
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Connect(); err != nil {
		t.Fatal(err)
	}
	req := tracker.AnnounceRequest{
		NumWant: -1,
		Event:   tracker.Started,
	}
	rand.Read(req.PeerId[:])
	copy(req.InfoHash[:], []uint8{0xa3, 0x56, 0x41, 0x43, 0x74, 0x23, 0xe6, 0x26, 0xd9, 0x38, 0x25, 0x4a, 0x6b, 0x80, 0x49, 0x10, 0xa6, 0x67, 0xa, 0xc1})
	_, err = tr.Announce(&req)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnnounceRandomInfoHash(t *testing.T) {
	wg := sync.WaitGroup{}
	for _, url := range []string{
		"udp://tracker.openbittorrent.com:80/announce",
		"udp://tracker.publicbt.com:80",
		"udp://tracker.istole.it:6969",
		"udp://tracker.ccc.de:80",
		"udp://tracker.open.demonii.com:1337",
	} {
		go func(url string) {
			defer wg.Done()
			tr, err := tracker.New(url)
			if err != nil {
				t.Fatal(err)
			}
			if err := tr.Connect(); err != nil {
				t.Log(err)
				return
			}
			req := tracker.AnnounceRequest{
				Event: tracker.Stopped,
			}
			rand.Read(req.PeerId[:])
			rand.Read(req.InfoHash[:])
			resp, err := tr.Announce(&req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Leechers != 0 || resp.Seeders != 0 || len(resp.Peers) != 0 {
				t.Fatal(resp)
			}
		}(url)
		wg.Add(1)
	}
	wg.Wait()
}
