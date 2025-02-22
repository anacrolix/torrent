package dht

import (
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/inproc"
	"github.com/anacrolix/sync"
	"github.com/anacrolix/torrent/bencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/krpc"
)

func TestSetNilBigInt(t *testing.T) {
	i := new(big.Int)
	i.SetBytes(make([]byte, 2))
}

func TestMarshalCompactNodeInfo(t *testing.T) {
	cni := krpc.CompactIPv4NodeInfo{krpc.NodeInfo{
		ID: [20]byte{'a', 'b', 'c'},
	}}
	addr, err := net.ResolveUDPAddr("udp4", "1.2.3.4:5")
	require.NoError(t, err)
	cni[0].Addr.FromUDPAddr(addr)
	cni[0].Addr.IP = cni[0].Addr.IP.To4()
	b, err := cni.MarshalBinary()
	require.NoError(t, err)
	var bb [26]byte
	copy(bb[:], []byte("abc"))
	copy(bb[20:], []byte("\x01\x02\x03\x04\x00\x05"))
	assert.EqualValues(t, string(bb[:]), string(b))
}

const zeroID = "\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"

var testIDs []int160.T

func init() {
	for _, s := range []string{
		zeroID,
		"\x03" + zeroID[1:],
		"\x03" + zeroID[1:18] + "\x55\xf0",
		"\x55" + zeroID[1:17] + "\xff\x55\x0f",
		"\x54" + zeroID[1:18] + "\x50\x0f",
	} {
		testIDs = append(testIDs, int160.FromByteString(s))
	}
	testIDs = append(testIDs, int160.T{})
}

func TestDistances(t *testing.T) {
	expectBitcount := func(i int160.T, count int) {
		if bitCount(i.Bytes()) != count {
			t.Fatalf("expected bitcount of %d: got %d", count, bitCount(i.Bytes()))
		}
	}
	expectBitcount(int160.Distance(testIDs[3], testIDs[0]), 4+8+4+4)
	expectBitcount(int160.Distance(testIDs[3], testIDs[1]), 4+8+4+4)
	expectBitcount(int160.Distance(testIDs[3], testIDs[2]), 4+8+8)
}

func TestMaxDistanceString(t *testing.T) {
	var max int160.T
	max.SetMax()
	require.EqualValues(t, "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff", max.Bytes())
}

// func TestClosestNodes(t *testing.T) {
// 	cn := newKClosestNodeIDs(2, testIDs[3])
// 	for _, i := range rand.Perm(len(testIDs)) {
// 		cn.Push(testIDs[i])
// 	}
// 	ids := iter.ToSlice(cn.IDs())
// 	assert.Len(t, ids, 2)
// 	m := map[string]bool{}
// 	for _, id := range ids {
// 		m[id.(nodeID).ByteString()] = true
// 	}
// 	log.Printf("%q", m)
// 	assert.True(t, m[testIDs[3].ByteString()])
// 	assert.True(t, m[testIDs[4].ByteString()])
// }

func TestDHTDefaultConfig(t *testing.T) {
	s, err := NewServer(nil)
	assert.NoError(t, err)
	s.Close()
}

func TestPing(t *testing.T) {
	recvConn := mustListen("127.0.0.1:0")
	srv, err := NewServer(&ServerConfig{
		Conn:        recvConn,
		NoSecurity:  true,
		Logger:      log.Default,
		WaitToReply: true,
	})
	srvUdpAddr := func(s *Server) *net.UDPAddr {
		return &net.UDPAddr{
			IP:   []byte{127, 0, 0, 1},
			Port: s.Addr().(*net.UDPAddr).Port,
		}
	}
	require.NoError(t, err)
	defer srv.Close()
	srv0, err := NewServer(&ServerConfig{
		Conn:          mustListen("127.0.0.1:0"),
		StartingNodes: addrResolver(srvUdpAddr(srv).String()),
		Logger:        log.Default,
		WaitToReply:   true,
	})
	require.NoError(t, err)
	defer srv0.Close()
	res := srv.Ping(srvUdpAddr(srv0))
	require.NoError(t, res.Err)
	require.EqualValues(t, srv0.ID(), *res.Reply.SenderID())
}

func TestServerCustomNodeId(t *testing.T) {
	idHex := "5a3ce1c14e7a08645677bbd1cfe7d8f956d53256"
	idBytes, err := hex.DecodeString(idHex)
	require.NoError(t, err)
	var id [20]byte
	n := copy(id[:], idBytes)
	require.Equal(t, 20, n)
	// How to test custom *secure* ID when tester computers will have
	// different IDs? Generate custom ids for local IPs and use mini-ID?
	s, err := NewServer(&ServerConfig{
		NodeId: id,
		Conn:   mustListen(":0"),
	})
	require.NoError(t, err)
	defer s.Close()
	assert.Equal(t, id, s.ID())
}

func TestAnnounceTimeout(t *testing.T) {
	s, err := NewServer(&ServerConfig{
		StartingNodes: addrResolver("1.2.3.4:5"),
		Conn:          mustListen(":0"),
		QueryResendDelay: func() time.Duration {
			return 0
		},
	})
	require.NoError(t, err)
	var ih [20]byte
	copy(ih[:], "12341234123412341234")
	a, err := s.Announce(ih, 0, true)
	assert.NoError(t, err)
	<-a.Peers
	a.Close()
	s.Close()
}

func TestEqualPointers(t *testing.T) {
	assert.EqualValues(t, &krpc.Msg{R: &krpc.Return{}}, &krpc.Msg{R: &krpc.Return{}})
}

func TestHook(t *testing.T) {
	pinger, err := NewServer(&ServerConfig{
		Conn:     mustListen("127.0.0.1:5678"),
		PublicIP: net.IPv4(127, 0, 0, 1),
	})
	require.NoError(t, err)
	defer pinger.Close()
	// Establish server with a hook attached to "ping"
	hookCalled := make(chan struct{}, 1)
	receiver, err := NewServer(&ServerConfig{
		Conn:          mustListen("127.0.0.1:5679"),
		PublicIP:      net.IPv4(127, 0, 0, 1),
		StartingNodes: addrResolver("127.0.0.1:5678"),
		OnQuery: func(m *krpc.Msg, addr net.Addr) bool {
			t.Logf("receiver got msg: %v", m)
			if m.Q == "ping" {
				select {
				case hookCalled <- struct{}{}:
				default:
				}
			}
			return true
		},
		WaitToReply: true,
	})
	require.NoError(t, err)
	defer receiver.Close()
	// Ping receiver from pinger to trigger hook. Should also receive a response.
	t.Log("TestHook: Servers created, hook for ping established. Calling Ping.")
	res := pinger.Ping(&net.UDPAddr{
		IP:   []byte{127, 0, 0, 1},
		Port: receiver.Addr().(*net.UDPAddr).Port,
	})
	assert.NoError(t, res.Err)
	// Await signal that hook has been called.
	select {
	case <-hookCalled:
		// Success, hook was triggered. TODO: Ensure that "ok" channel
		// receives, also, indicating normal handling proceeded also.
		t.Log("TestHook: Received ping, hook called and returned to normal execution!")
		t.Log("TestHook: Sender received response from pinged hook server, so normal execution resumed.")
	case <-time.After(time.Second * 1):
		t.Error("Failed to see evidence of ping hook being called after 2 seconds.")
	}
}

// Check that address resolution doesn't rat out invalid SendTo addr
// arguments.
func TestResolveBadAddr(t *testing.T) {
	ua, err := net.ResolveUDPAddr("udp", "0.131.255.145:33085")
	require.NoError(t, err)
	assert.False(t, validNodeAddr(ua))
}

func TestGlobalBootstrapAddrs(t *testing.T) {
	addrs, err := GlobalBootstrapAddrs("udp")
	if err != nil {
		t.Skip(err)
	}
	for _, a := range addrs {
		t.Log(a)
	}
}

// https://github.com/anacrolix/dht/pull/19
func TestBadGetPeersResponse(t *testing.T) {
	pc, err := net.ListenPacket("udp", "localhost:0")
	require.NoError(t, err)
	defer pc.Close()
	s, err := NewServer(&ServerConfig{
		StartingNodes: func() ([]Addr, error) {
			return []Addr{NewAddr(pc.LocalAddr().(*net.UDPAddr))}, nil
		},
		Conn: mustListen("localhost:0"),
	})
	require.NoError(t, err)
	defer s.Close()
	go func() {
		b := make([]byte, 1024)
		n, addr, err := pc.ReadFrom(b)
		require.NoError(t, err)
		var rm krpc.Msg
		bencode.Unmarshal(b[:n], &rm)
		m := krpc.Msg{
			R: &krpc.Return{},
			T: rm.T,
		}
		b, err = bencode.Marshal(m)
		require.NoError(t, err)
		pc.WriteTo(b, addr)
	}()
	a, err := s.Announce([20]byte{}, 0, true)
	require.NoError(t, err)
	// Drain the Announce until it closes.
	for range a.Peers {
	}
}

func TestBootstrapRace(t *testing.T) {
	remotePc, err := inproc.ListenPacket("", "localhost:0")
	require.NoError(t, err)
	defer remotePc.Close()
	serverPc := bootstrapRacePacketConn{
		read: make(chan read),
	}
	t.Logf("remote addr: %s", remotePc.LocalAddr())
	s, err := NewServer(&ServerConfig{
		Conn:             &serverPc,
		StartingNodes:    addrResolver(remotePc.LocalAddr().String()),
		QueryResendDelay: func() time.Duration { return 0 },
		Logger:           log.Default,
	})
	require.NoError(t, err)
	defer s.Close()
	go func() {
		for i := 0; i < defaultMaxQuerySends-1; i++ {
			remotePc.ReadFrom(nil)
		}
		var b [1024]byte
		_, addr, _ := remotePc.ReadFrom(b[:])
		var m krpc.Msg
		bencode.Unmarshal(b[:], &m)
		m.Y = "r"
		rb, err := bencode.Marshal(m)
		if err != nil {
			panic(err)
		}
		remotePc.WriteTo(rb, addr)
	}()
	ts, err := s.Bootstrap()
	t.Logf("%#v", ts)
	require.NoError(t, err)
}

type emptyNetAddr struct{}

func (emptyNetAddr) Network() string { return "" }
func (emptyNetAddr) String() string  { return "" }

type read struct {
	b    []byte
	addr net.Addr
}

type bootstrapRacePacketConn struct {
	mu     sync.Mutex
	writes int
	read   chan read
}

func (me *bootstrapRacePacketConn) Close() error {
	close(me.read)
	return nil
}
func (me *bootstrapRacePacketConn) LocalAddr() net.Addr { return emptyNetAddr{} }
func (me *bootstrapRacePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	r, ok := <-me.read
	if !ok {
		return 0, nil, io.EOF
	}
	copy(b, r.b)
	log.Printf("reading %q from %s", r.b, r.addr)
	return len(r.b), r.addr, nil
}
func (me *bootstrapRacePacketConn) SetDeadline(time.Time) error      { return nil }
func (me *bootstrapRacePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (me *bootstrapRacePacketConn) SetWriteDeadline(time.Time) error { return nil }

func (me *bootstrapRacePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.writes++
	log.Printf("wrote %d times", me.writes)
	if me.writes == defaultMaxQuerySends {
		var m krpc.Msg
		bencode.Unmarshal(b[:], &m)
		m.Y = "r"
		rb, err := bencode.Marshal(m)
		if err != nil {
			panic(err)
		}
		me.read <- read{rb, addr}
		return 0, errors.New("write error")
	}
	return len(b), nil
}
