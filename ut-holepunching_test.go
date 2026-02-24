package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/iter"
	qt "github.com/go-quicktest/qt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/internal/testutil"
)

// Check that after completing leeching, a leecher transitions to a seeding
// correctly. Connected in a chain like so: Seeder <-> Leecher <-> LeecherLeecher.
func TestHolepunchConnect(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	cfg := TestingConfig(t)
	cfg.Seed = true
	cfg.MaxAllocPeerRequestDataPerConn = 4
	cfg.DataDir = greetingTempDir
	cfg.DisablePEX = true
	cfg.Debug = true
	cfg.AcceptPeerConnections = false
	// Listening, even without accepting, still means the leecher-leecher completes the dial to the
	// seeder, and so it won't attempt to holepunch.
	cfg.DisableTCP = true
	// Ensure that responding to holepunch connects don't wait around for the dial limit. We also
	// have to allow the initial connection to the leecher though, so it can rendezvous for us.
	cfg.DialRateLimiter = rate.NewLimiter(0, 1)
	cfg.Logger = cfg.Logger.WithContextText("seeder")
	seeder, err := NewClient(cfg)
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s", t)()
	seederTorrent, ok, err := seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	assert.True(t, ok)
	seederTorrent.VerifyData()

	cfg = TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = t.TempDir()
	cfg.AlwaysWantConns = true
	cfg.Logger = cfg.Logger.WithContextText("leecher")
	// This way the leecher leecher will still try to use this peer as a relay, but won't be told
	// about the seeder via PEX.
	//cfg.DisablePEX = true
	cfg.Debug = true
	leecher, err := NewClient(cfg)
	require.NoError(t, err)
	defer leecher.Close()
	defer testutil.ExportStatusWriter(leecher, "l", t)()

	cfg = TestingConfig(t)
	cfg.Seed = false
	cfg.DataDir = t.TempDir()
	cfg.MaxAllocPeerRequestDataPerConn = 4
	cfg.Debug = true
	cfg.NominalDialTimeout = time.Second
	cfg.Logger = cfg.Logger.WithContextText("leecher-leecher")
	//cfg.DisableUTP = true
	leecherLeecher, _ := NewClient(cfg)
	require.NoError(t, err)
	defer leecherLeecher.Close()
	defer testutil.ExportStatusWriter(leecherLeecher, "ll", t)()
	leecherGreeting, ok, err := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	_ = leecherGreeting
	require.NoError(t, err)
	assert.True(t, ok)
	llg, ok, err := leecherLeecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 3
		return
	}())
	require.NoError(t, err)
	assert.True(t, ok)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := llg.NewReader()
		defer r.Close()
		qt.Check(t, qt.IsNil(iotest.TestReader(r, []byte(testutil.GreetingFileContents))))
	}()
	go seederTorrent.AddClientPeer(leecher)
	waitForConns(seederTorrent)
	go llg.AddClientPeer(leecher)
	waitForConns(llg)
	time.Sleep(time.Second)
	llg.cl.lock()
	targetAddr := seeder.ListenAddrs()[0]
	log.Printf("trying to initiate to %v", targetAddr)
	initiateConn(outgoingConnOpts{
		peerInfo: PeerInfo{
			Addr: targetAddr,
		},
		t:                       llg,
		requireRendezvous:       true,
		skipHolepunchRendezvous: false,
		HeaderObfuscationPolicy: llg.cl.config.HeaderObfuscationPolicy,
	}, true)
	llg.cl.unlock()
	wg.Wait()

	qt.Check(t, qt.Not(qt.HasLen(seeder.dialedSuccessfullyAfterHolepunchConnect, 0)))
	qt.Check(t, qt.Not(qt.HasLen(leecherLeecher.probablyOnlyConnectedDueToHolepunch, 0)))

	llClientStats := leecherLeecher.Stats()
	qt.Check(t, qt.Not(qt.Equals(llClientStats.NumPeersUndialableWithoutHolepunch, 0)))
	qt.Check(t, qt.Not(qt.Equals(llClientStats.NumPeersUndialableWithoutHolepunchDialedAfterHolepunchConnect, 0)))
	qt.Check(t, qt.Not(qt.Equals(llClientStats.NumPeersProbablyOnlyConnectedDueToHolepunch, 0)))
}

func waitForConns(t *Torrent) {
	t.cl.lock()
	defer t.cl.unlock()
	for {
		for range t.conns {
			return
		}
		t.cl.event.Wait()
	}
}

// Show that dialling TCP will complete before the other side accepts.
func TestDialTcpNotAccepting(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:0")
	qt.Check(t, qt.IsNil(err))
	defer l.Close()
	dialedConn, err := net.Dial("tcp", l.Addr().String())
	qt.Assert(t, qt.IsNil(err))
	dialedConn.Close()
}

func TestTcpSimultaneousOpen(t *testing.T) {
	const network = "tcp"
	ctx := context.Background()
	makeDialer := func(localPort int, remoteAddr string) func() (net.Conn, error) {
		dialer := net.Dialer{
			LocalAddr: &net.TCPAddr{
				//IP:   net.IPv6loopback,
				Port: localPort,
			},
		}
		return func() (net.Conn, error) {
			return dialer.DialContext(ctx, network, remoteAddr)
		}
	}
	// I really hate doing this in unit tests, but we would need to pick apart Dialer to get
	// perfectly synchronized simultaneous dials.
	for range iter.N(10) {
		first, second := randPortPair()
		t.Logf("ports are %v and %v", first, second)
		err := testSimultaneousOpen(
			t.Cleanup,
			makeDialer(first, fmt.Sprintf("localhost:%d", second)),
			makeDialer(second, fmt.Sprintf("localhost:%d", first)),
		)
		if err == nil {
			return
		}
		// This proves that the connections are not the same.
		if errors.Is(err, errMsgNotReceived) {
			t.Fatal(err)
		}
		// Could be a timing issue, so try again.
		t.Log(err)
	}
	// If we weren't able to get a simultaneous dial to occur, then we can't call it a failure.
	t.Skip("couldn't synchronize dials")
}

func randIntInRange(low, high int) int {
	return rand.Intn(high-low+1) + low
}

func randDynamicPort() int {
	return randIntInRange(49152, 65535)
}

func randPortPair() (first int, second int) {
	first = randDynamicPort()
	for {
		second = randDynamicPort()
		if second != first {
			return
		}
	}
}

func writeMsg(conn net.Conn) {
	conn.Write([]byte(defaultMsg))
	// Writing must be closed so the reader will get EOF and stop reading.
	conn.Close()
}

func readMsg(conn net.Conn) error {
	msgBytes, err := io.ReadAll(conn)
	if err != nil {
		return err
	}
	msgStr := string(msgBytes)
	if msgStr != defaultMsg {
		return fmt.Errorf("read %q", msgStr)
	}
	return nil
}

var errMsgNotReceived = errors.New("msg not received in time")

// Runs two dialers simultaneously, then sends a message on one connection and check it reads from
// the other, thereby showing that both dials obtained endpoints to the same connection.
func testSimultaneousOpen(
	cleanup func(func()),
	firstDialer, secondDialer func() (net.Conn, error),
) error {
	errs := make(chan error)
	var dialsDone sync.WaitGroup
	const numDials = 2
	dialsDone.Add(numDials)
	signal := make(chan struct{})
	var dialersDone sync.WaitGroup
	dialersDone.Add(numDials)
	doDial := func(
		dialer func() (net.Conn, error),
		onSignal func(net.Conn),
	) {
		defer dialersDone.Done()
		conn, err := dialer()
		dialsDone.Done()
		errs <- err
		if err != nil {
			return
		}
		cleanup(func() {
			conn.Close()
		})
		<-signal
		onSignal(conn)
		//if err == nil {
		//	conn.Close()
		//}
	}
	go doDial(
		firstDialer,
		func(conn net.Conn) {
			writeMsg(conn)
			errs <- nil
		},
	)
	go doDial(
		secondDialer,
		func(conn net.Conn) {
			gotMsg := make(chan error, 1)
			go func() {
				gotMsg <- readMsg(conn)
			}()
			select {
			case err := <-gotMsg:
				errs <- err
			case <-time.After(time.Second):
				errs <- errMsgNotReceived
			}
		},
	)
	dialsDone.Wait()
	for range iter.N(numDials) {
		err := <-errs
		if err != nil {
			return err
		}
	}
	close(signal)
	for range iter.N(numDials) {
		err := <-errs
		if err != nil {
			return err
		}
	}
	dialersDone.Wait()
	return nil
}

const defaultMsg = "hello"

// Show that uTP doesn't implement simultaneous open. When two sockets dial each other, they both
// get separate connections. This means that holepunch connect may result in an accept (and dial)
// for one or both peers involved.
func TestUtpSimultaneousOpen(t *testing.T) {
	t.Parallel()
	const network = "udp"
	ctx := context.Background()
	newUtpSocket := func(addr string) utpSocket {
		socket, err := NewUtpSocket(
			network,
			addr,
			func(net.Addr) bool {
				return false
			},
			log.Default,
		)
		qt.Assert(t, qt.IsNil(err))
		return socket
	}
	first := newUtpSocket("localhost:0")
	defer first.Close()
	second := newUtpSocket("localhost:0")
	defer second.Close()
	getDial := func(sock utpSocket, addr string) func() (net.Conn, error) {
		return func() (net.Conn, error) {
			return sock.DialContext(ctx, network, addr)
		}
	}
	t.Logf("first addr is %v. second addr is %v", first.Addr().String(), second.Addr().String())
	for range iter.N(10) {
		err := testSimultaneousOpen(
			t.Cleanup,
			getDial(first, second.Addr().String()),
			getDial(second, first.Addr().String()),
		)
		if err == nil {
			t.Fatal("expected utp to fail simultaneous open")
		}
		if errors.Is(err, errMsgNotReceived) {
			return
		}
		skipGoUtpDialIssue(t, err)
		t.Log(err)
		time.Sleep(time.Second)
	}
	t.FailNow()
}

func writeAndReadMsg(r, w net.Conn) error {
	go writeMsg(w)
	return readMsg(r)
}

func skipGoUtpDialIssue(t *testing.T, err error) {
	if err.Error() == "timed out waiting for ack" {
		t.Skip("anacrolix go utp implementation has issues. Use anacrolix/go-libutp by enabling CGO.")
	}
}

// Show that dialling one socket and accepting from the other results in them having ends of the
// same connection.
func TestUtpDirectDialMsg(t *testing.T) {
	t.Parallel()
	const network = "udp4"
	ctx := context.Background()
	newUtpSocket := func(addr string) utpSocket {
		socket, err := NewUtpSocket(network, addr, func(net.Addr) bool {
			return false
		}, log.Default)
		qt.Assert(t, qt.IsNil(err))
		return socket
	}
	for range iter.N(10) {
		err := func() error {
			first := newUtpSocket("localhost:0")
			defer first.Close()
			second := newUtpSocket("localhost:0")
			defer second.Close()
			writer, err := first.DialContext(ctx, network, second.Addr().String())
			if err != nil {
				return err
			}
			defer writer.Close()
			reader, err := second.Accept()
			qt.Assert(t, qt.IsNil(err))
			defer reader.Close()
			return writeAndReadMsg(reader, writer)
		}()
		if err == nil {
			return
		}
		skipGoUtpDialIssue(t, err)
		t.Log(err)
		time.Sleep(time.Second)
	}
	t.FailNow()
}
