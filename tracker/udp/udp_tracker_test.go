package udp_tracker

import (
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
)

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
	tr, err := tracker.New(func() *url.URL {
		u, err := url.Parse("udp://tracker.openbittorrent.com:80/announce")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}())
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
	n, err := hex.Decode(req.InfoHash[:], []byte("c833bb2b5e7bcb9c07f4c020b4be430c28ba7cdb"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(req.InfoHash) {
		panic("nope")
	}
	resp, err := tr.Announce(&req)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(resp)
}
