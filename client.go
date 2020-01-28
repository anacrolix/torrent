package torrent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	l2 "log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/perf"
	"github.com/anacrolix/missinggo/pproffd"
	"github.com/anacrolix/missinggo/pubsub"
	"github.com/anacrolix/missinggo/slices"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/conntrack"
	"github.com/anacrolix/sync"
	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	"github.com/google/btree"
	"golang.org/x/time/rate"
	"golang.org/x/xerrors"

	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

var _ = l2.Print
var _ = stdsync.Mutex{}

// Client contain zero or more Torrents. A Client manages a blocklist, the
// TCP/UDP protocol ports, and DHT as desired.
type Client struct {
	// An aggregate of stats over all connections. First in struct to ensure
	// 64-bit alignment of fields. See #262.
	stats ConnStats

	_mu    lockWithDeferreds
	event  sync.Cond
	closed missinggo.Event

	config *ClientConfig
	logger log.Logger

	peerID         PeerID
	defaultStorage *storage.Client
	onClose        []func()
	conns          []socket
	dhtServers     []*dht.Server
	ipBlockList    iplist.Ranger
	// Our BitTorrent protocol extension bytes, sent in our BT handshakes.
	extensionBytes pp.PeerExtensionBits

	// Set of addresses that have our client ID. This intentionally will
	// include ourselves if we end up trying to connect to our own address
	// through legitimate channels.
	dopplegangerAddrs map[string]struct{}
	badPeerIPs        map[string]struct{}
	torrents          map[InfoHash]*torrent

	acceptLimiter   map[ipStr]int
	dialRateLimiter *rate.Limiter
}

type ipStr string

// BadPeerIPs ...
func (cl *Client) BadPeerIPs() []string {
	cl.rLock()
	defer cl.rUnlock()
	return cl.badPeerIPsLocked()
}

// Start the specified torrent.
// Start adds starts up the torrent within the client downloading the missing pieces
// as needed. if you want to wait until the torrent is completed use Download.
func (cl *Client) Start(t Metadata) (dl Torrent, added bool, err error) {
	return cl.start(t)
}

// MaybeStart is a convience method that consumes the return types of the torrent
// creation methods: New, NewFromFile, etc. it is all respects identical to the Start
// method.
func (cl *Client) MaybeStart(t Metadata, failed error) (dl Torrent, added bool, err error) {
	if failed != nil {
		return dl, false, failed
	}
	return cl.start(t)
}

func (cl *Client) start(t Metadata) (dlt *torrent, added bool, err error) {
	if dlt = cl.download(t); dlt != nil {
		return dlt, false, cl.merge(dlt, t)
	}

	cl.lock()
	dlt = cl.newTorrent(t)
	cl.unlock()

	cl.merge(dlt, t)

	cl.lock()
	defer cl.unlock()
	cl.eachDhtServer(func(s *dht.Server) {
		go dlt.dhtAnnouncer(s)
	})
	cl.torrents[t.InfoHash] = dlt
	cl.clearAcceptLimits()
	dlt.updateWantPeersEvent()
	// Tickle Client.waitAccept, new torrent may want conns.
	cl.event.Broadcast()

	return dlt, true, nil
}

// Stop the specified torrent, this halts all network activity around the torrent
// for this client.
func (cl *Client) Stop(t Metadata) (err error) {
	return cl.dropTorrent(t.InfoHash)
}

func (cl *Client) merge(old *torrent, updated Metadata) (err error) {
	if updated.DisplayName != "" {
		old.SetDisplayName(updated.DisplayName)
	}

	cl.lock()
	defer cl.unlock()

	if updated.ChunkSize != 0 {
		old.setChunkSize(pp.Integer(updated.ChunkSize))
	}

	old.addTrackers(updated.Trackers)
	old.maybeNewConns()
	return nil
}

func (cl *Client) download(t Metadata) *torrent {
	cl.lock()
	defer cl.unlock()

	if dl, ok := cl.torrents[t.InfoHash]; ok {
		return dl
	}

	return nil
}

func (cl *Client) badPeerIPsLocked() []string {
	return slices.FromMapKeys(cl.badPeerIPs).([]string)
}

// PeerID ...
func (cl *Client) PeerID() PeerID {
	return cl.peerID
}

// LocalPort returns the local port being listened on.
// WARNING: this method can panic.
// this is method is odd given a client can be attached to multiple ports on different
// listeners.
func (cl *Client) LocalPort() (port int) {
	cl.eachListener(func(l socket) bool {
		_port := missinggo.AddrPort(l.Addr())
		if _port == 0 {
			panic(l)
		}
		if port == 0 {
			port = _port
		} else if port != _port {
			panic("mismatched ports")
		}
		return true
	})
	return
}

func writeDhtServerStatus(w io.Writer, s *dht.Server) {
	dhtStats := s.Stats()
	fmt.Fprintf(w, "\t# Nodes: %d (%d good, %d banned)\n", dhtStats.Nodes, dhtStats.GoodNodes, dhtStats.BadNodes)
	fmt.Fprintf(w, "\tServer ID: %x\n", s.ID())
	fmt.Fprintf(w, "\tAnnounces: %d\n", dhtStats.SuccessfulOutboundAnnouncePeerQueries)
	fmt.Fprintf(w, "\tOutstanding transactions: %d\n", dhtStats.OutstandingTransactions)
}

// WriteStatus writes out a human readable status of the client, such as for writing to a
// HTTP status page.
func (cl *Client) WriteStatus(_w io.Writer) {
	cl.rLock()
	defer cl.rUnlock()
	w := bufio.NewWriter(_w)
	defer w.Flush()
	fmt.Fprintf(w, "Listen port: %d\n", cl.LocalPort())
	fmt.Fprintf(w, "Peer ID: %+q\n", cl.PeerID())
	fmt.Fprintf(w, "Announce key: %x\n", cl.announceKey())
	fmt.Fprintf(w, "Banned IPs: %d\n", len(cl.badPeerIPsLocked()))
	cl.eachDhtServer(func(s *dht.Server) {
		fmt.Fprintf(w, "%s DHT server at %s:\n", s.Addr().Network(), s.Addr().String())
		writeDhtServerStatus(w, s)
	})
	spew.Fdump(w, &cl.stats)
	fmt.Fprintf(w, "# Torrents: %d\n", len(cl.torrentsAsSlice()))
	fmt.Fprintln(w)
	for _, t := range slices.Sort(cl.torrentsAsSlice(), func(l, r *torrent) bool {
		return l.InfoHash().AsString() < r.InfoHash().AsString()
	}).([]*torrent) {
		if t.name() == "" {
			fmt.Fprint(w, "<unknown name>")
		} else {
			fmt.Fprint(w, t.name())
		}
		fmt.Fprint(w, "\n")
		if t.info != nil {
			fmt.Fprintf(w, "%f%% of %d bytes (%s)", 100*(1-float64(t.BytesMissing())/float64(t.info.TotalLength())), t.info.Length, humanize.Bytes(uint64(t.info.TotalLength())))
		} else {
			w.WriteString("<missing metainfo>")
		}
		fmt.Fprint(w, "\n")
		t.writeStatus(w)
		fmt.Fprintln(w)
	}
}

const debugLogValue = log.Debug

func (cl *Client) debugLogFilter(m log.Msg) bool {
	if cl.config.Debug {
		return true
	}
	return !m.HasValue(debugLogValue)
}

func (cl *Client) initLogger() {
	cl.logger = cl.config.Logger.WithValues(cl).WithFilter(cl.debugLogFilter)
}

func (cl *Client) announceKey() int32 {
	return int32(binary.BigEndian.Uint32(cl.peerID[16:20]))
}

func NewClient(cfg *ClientConfig) (cl *Client, err error) {
	if cfg == nil {
		cfg = NewDefaultClientConfig()
		cfg.ListenPort = 0
	}
	defer func() {
		if err != nil {
			cl = nil
		}
	}()
	cl = &Client{
		config:            cfg,
		dopplegangerAddrs: make(map[string]struct{}),
		torrents:          make(map[metainfo.Hash]*torrent),
		dialRateLimiter:   rate.NewLimiter(10, 10),
	}
	go cl.acceptLimitClearer()
	cl.initLogger()
	defer func() {
		if err == nil {
			return
		}
		cl.Close()
	}()
	cl.extensionBytes = defaultPeerExtensionBytes()
	cl.event.L = cl.locker()
	storageImpl := cfg.DefaultStorage
	if storageImpl == nil {
		// We'd use mmap but HFS+ doesn't support sparse files.
		storageImpl = storage.NewFile(cfg.DataDir)
		cl.onClose = append(cl.onClose, func() {
			if err := storageImpl.Close(); err != nil {
				cl.logger.Printf("error closing default storage: %s", err)
			}
		})
	}
	cl.defaultStorage = storage.NewClient(storageImpl)
	if cfg.IPBlocklist != nil {
		cl.ipBlockList = cfg.IPBlocklist
	}

	if cfg.PeerID != "" {
		missinggo.CopyExact(&cl.peerID, cfg.PeerID)
	} else {
		o := copy(cl.peerID[:], cfg.Bep20)
		_, err = rand.Read(cl.peerID[o:])
		if err != nil {
			panic("error generating peer id")
		}
	}

	if cl.config.HTTPProxy == nil && cl.config.ProxyURL != "" {
		if fixedURL, err := url.Parse(cl.config.ProxyURL); err == nil {
			cl.config.HTTPProxy = http.ProxyURL(fixedURL)
		}
	}

	cl.conns, err = listenAll(cl.listenNetworks(), cl.config.ListenHost, cl.config.ListenPort, cl.config.ProxyURL, cl.firewallCallback)
	if err != nil {
		return
	}
	// Check for panics.
	cl.LocalPort()

	for _, s := range cl.conns {
		if peerNetworkEnabled(parseNetworkString(s.Addr().Network()), cl.config) {
			go cl.acceptConnections(s)
		}
	}

	go cl.forwardPort()
	if !cfg.NoDHT {
		for _, s := range cl.conns {
			if pc, ok := s.(net.PacketConn); ok {
				ds, err := cl.newDhtServer(pc)
				if err != nil {
					panic(err)
				}
				cl.dhtServers = append(cl.dhtServers, ds)
			}
		}
	}

	return
}

func (cl *Client) firewallCallback(net.Addr) bool {
	// cl.rLock()
	block := !cl.wantConns()
	// cl.rUnlock()
	if block {
		metrics.Add("connections firewalled", 1)
	} else {
		metrics.Add("connections not firewalled", 1)
	}
	return block
}

func (cl *Client) enabledPeerNetworks() (ns []network) {
	for _, n := range allPeerNetworks {
		if peerNetworkEnabled(n, cl.config) {
			ns = append(ns, n)
		}
	}
	return
}

func (cl *Client) listenOnNetwork(n network) bool {
	if n.Ipv4 && cl.config.DisableIPv4 {
		return false
	}
	if n.Ipv6 && cl.config.DisableIPv6 {
		return false
	}
	if n.Tcp && cl.config.DisableTCP {
		return false
	}
	if n.Udp && cl.config.DisableUTP && cl.config.NoDHT {
		return false
	}
	return true
}

func (cl *Client) listenNetworks() (ns []network) {
	for _, n := range allPeerNetworks {
		if cl.listenOnNetwork(n) {
			ns = append(ns, n)
		}
	}
	return
}

func (cl *Client) newDhtServer(conn net.PacketConn) (s *dht.Server, err error) {
	cfg := dht.ServerConfig{
		IPBlocklist:    cl.ipBlockList,
		Conn:           conn,
		OnAnnouncePeer: cl.onDHTAnnouncePeer,
		PublicIP: func() net.IP {
			if connIsIpv6(conn) && cl.config.PublicIp6 != nil {
				return cl.config.PublicIp6
			}
			return cl.config.PublicIp4
		}(),
		StartingNodes:      cl.config.DhtStartingNodes,
		ConnectionTracking: cl.config.ConnTracker,
		OnQuery:            cl.config.DHTOnQuery,
		Logger:             cl.logger.WithValues("dht", conn.LocalAddr().String()),
	}
	s, err = dht.NewServer(&cfg)
	if err == nil {
		go func() {
			ts, err := s.Bootstrap()
			if err != nil {
				cl.logger.Printf("error bootstrapping dht: %s", err)
			}
			log.Fstr("%v completed bootstrap (%v)", s, ts).AddValues(s, ts).Log(cl.logger)
		}()
	}
	return
}

func (cl *Client) Closed() <-chan struct{} {
	cl.lock()
	defer cl.unlock()
	return cl.closed.C()
}

func (cl *Client) eachDhtServer(f func(*dht.Server)) {
	for _, ds := range cl.dhtServers {
		f(ds)
	}
}

func (cl *Client) closeSockets() {
	cl.eachListener(func(l socket) bool {
		l.Close()
		return true
	})
	cl.conns = nil
}

// Close stops the client. All connections to peers are closed and all activity will
// come to a halt.
func (cl *Client) Close() {
	cl.closed.Set()
	cl.eachDhtServer(func(s *dht.Server) { s.Close() })
	cl.closeSockets()

	for _, t := range cl.torrents {
		t.close()
	}

	for _, f := range cl.onClose {
		f()
	}
	cl.event.Broadcast()
}

func (cl *Client) ipBlockRange(ip net.IP) (r iplist.Range, blocked bool) {
	if cl.ipBlockList == nil {
		return
	}
	return cl.ipBlockList.Lookup(ip)
}

func (cl *Client) ipIsBlocked(ip net.IP) bool {
	_, blocked := cl.ipBlockRange(ip)
	return blocked
}

func (cl *Client) wantConns() bool {
	for _, t := range cl.torrents {
		if t.wantConns() {
			return true
		}
	}
	return false
}

func (cl *Client) waitAccept() {
	for {
		if cl.closed.IsSet() {
			return
		}
		if cl.wantConns() {
			return
		}
		cl.event.Wait()
	}
}

func (cl *Client) rejectAccepted(conn net.Conn) error {
	ra := conn.RemoteAddr()
	rip := missinggo.AddrIP(ra)
	if cl.config.DisableIPv4Peers && rip.To4() != nil {
		return errors.New("ipv4 peers disabled")
	}
	if cl.config.DisableIPv4 && len(rip) == net.IPv4len {
		return errors.New("ipv4 disabled")

	}
	if cl.config.DisableIPv6 && len(rip) == net.IPv6len && rip.To4() == nil {
		return errors.New("ipv6 disabled")
	}
	if cl.rateLimitAccept(rip) {
		return errors.New("source IP accepted rate limited")
	}
	if cl.badPeerIPPort(rip, missinggo.AddrPort(ra)) {
		return errors.New("bad source addr")
	}
	return nil
}

func (cl *Client) acceptConnections(l net.Listener) {
	for {
		conn, err := l.Accept()
		metrics.Add("client listener accepts", 1)
		conn = pproffd.WrapNetConn(conn)
		cl.rLock()
		closed := cl.closed.IsSet()
		var reject error
		if conn != nil {
			reject = cl.rejectAccepted(conn)
		}
		cl.rUnlock()
		if closed {
			if conn != nil {
				conn.Close()
			}
			return
		}
		if err != nil {
			log.Fmsg("error accepting connection: %s", err).AddValue(debugLogValue).Log(cl.logger)
			continue
		}
		go func() {
			if reject != nil {
				metrics.Add("rejected accepted connections", 1)
				log.Fmsg("rejecting accepted conn: %v", reject).AddValue(debugLogValue).Log(cl.logger)
				conn.Close()
			} else {
				go cl.incomingConnection(conn)
			}
			log.Fmsg("accepted %s connection from %s", conn.RemoteAddr().Network(), conn.RemoteAddr()).AddValue(debugLogValue).Log(cl.logger)
			metrics.Add(fmt.Sprintf("accepted conn remote IP len=%d", len(missinggo.AddrIP(conn.RemoteAddr()))), 1)
			metrics.Add(fmt.Sprintf("accepted conn network=%s", conn.RemoteAddr().Network()), 1)
			metrics.Add(fmt.Sprintf("accepted on %s listener", l.Addr().Network()), 1)
		}()
	}
}

func (cl *Client) incomingConnection(nc net.Conn) {
	defer nc.Close()
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c := cl.newConnection(nc, false, missinggo.IpPortFromNetAddr(nc.RemoteAddr()), nc.RemoteAddr().Network())
	c.Discovery = peerSourceIncoming
	cl.runReceivedConn(c)
}

// Torrent returns a handle to the given torrent, if it's present in the client.
func (cl *Client) Torrent(ih metainfo.Hash) (t *torrent, ok bool) {
	cl.lock()
	defer cl.unlock()
	t, ok = cl.torrents[ih]
	return
}

func (cl *Client) torrent(ih metainfo.Hash) *torrent {
	return cl.torrents[ih]
}

type dialResult struct {
	Conn    net.Conn
	Network string
}

func countDialResult(err error) {
	if err == nil {
		metrics.Add("successful dials", 1)
	} else {
		metrics.Add("unsuccessful dials", 1)
	}
}

func reducedDialTimeout(minDialTimeout, max time.Duration, halfOpenLimit int, pendingPeers int) (ret time.Duration) {
	ret = max / time.Duration((pendingPeers+halfOpenLimit)/halfOpenLimit)
	if ret < minDialTimeout {
		ret = minDialTimeout
	}
	return
}

// Returns whether an address is known to connect to a client with our own ID.
func (cl *Client) dopplegangerAddr(addr string) bool {
	_, ok := cl.dopplegangerAddrs[addr]
	return ok
}

// Returns a connection over UTP or TCP, whichever is first to connect.
func (cl *Client) dialFirst(ctx context.Context, addr string) (res dialResult) {
	{
		t := perf.NewTimer(perf.CallerName(0))
		defer func() {
			if res.Conn == nil {
				t.Mark(fmt.Sprintf("returned no conn (context: %v)", ctx.Err()))
			} else {
				t.Mark("returned conn over " + res.Network)
			}
		}()
	}
	ctx, cancel := context.WithCancel(ctx)
	// As soon as we return one connection, cancel the others.
	defer cancel()
	left := 0
	resCh := make(chan dialResult, left)
	func() {
		cl.lock()
		defer cl.unlock()
		cl.eachListener(func(s socket) bool {
			func() {
				network := s.Addr().Network()
				if !peerNetworkEnabled(parseNetworkString(network), cl.config) {
					return
				}
				left++
				//cl.logger.Printf("dialing %s on %s/%s", addr, s.Addr().Network(), s.Addr())
				go func() {
					resCh <- dialResult{
						cl.dialFromSocket(ctx, s, addr),
						network,
					}
				}()
			}()
			return true
		})
	}()
	// Wait for a successful connection.
	func() {
		defer perf.ScopeTimer()()
		for ; left > 0 && res.Conn == nil; left-- {
			res = <-resCh
		}
	}()
	// There are still incompleted dials.
	go func() {
		for ; left > 0; left-- {
			conn := (<-resCh).Conn
			if conn != nil {
				conn.Close()
			}
		}
	}()

	if res.Conn != nil {
		// why is this spawning a go routine.... any why is it being tracked? seems like a good way to consume memory.
		go metrics.Add(fmt.Sprintf("network dialed first: %s", res.Conn.RemoteAddr().Network()), 1)
	}
	//if res.Conn != nil {
	//	cl.logger.Printf("first connection for %s from %s/%s", addr, res.Conn.LocalAddr().Network(), res.Conn.LocalAddr().String())
	//} else {
	//	cl.logger.Printf("failed to dial %s", addr)
	//}
	return res
}

func (cl *Client) dialFromSocket(ctx context.Context, s socket, addr string) net.Conn {
	network := s.Addr().Network()
	cte := cl.config.ConnTracker.Wait(
		ctx,
		conntrack.Entry{Protocol: network, LocalAddr: s.Addr().String(), RemoteAddr: addr},
		"dial torrent client",
		0,
	)
	// Try to avoid committing to a dial if the context is complete as it's difficult to determine
	// which dial errors allow us to forget the connection tracking entry handle.
	if ctx.Err() != nil {
		if cte != nil {
			cte.Forget()
		}
		return nil
	}
	c, err := s.dial(ctx, addr)
	// This is a bit optimistic, but it looks non-trivial to thread this through the proxy code. Set
	// it now in case we close the connection forthwith.
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	countDialResult(err)
	if c == nil {
		if err != nil && forgettableDialError(err) {
			cte.Forget()
		} else {
			cte.Done()
		}
		return nil
	}
	return closeWrapper{c, func() error {
		err := c.Close()
		cte.Done()
		return err
	}}
}

func forgettableDialError(err error) bool {
	return strings.Contains(err.Error(), "no suitable address found")
}

func (cl *Client) noLongerHalfOpen(t *torrent, addr string) {
	if _, ok := t.halfOpen[addr]; !ok {
		panic("invariant broken")
	}
	delete(t.halfOpen, addr)

	t.lockedOpenNewConns()
}

// Performs initiator handshakes and returns a connection. Returns nil
// *connection if no connection for valid reasons.
func (cl *Client) handshakesConnection(ctx context.Context, nc net.Conn, t *torrent, encryptHeader bool, remoteAddr IpPort, network string) (c *connection, err error) {
	c = cl.newConnection(nc, true, remoteAddr, network)
	c.headerEncrypted = encryptHeader
	ctx, cancel := context.WithTimeout(ctx, cl.config.HandshakesTimeout)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		panic(ctx)
	}
	err = nc.SetDeadline(dl)
	if err != nil {
		panic(err)
	}
	err = cl.initiateHandshakes(c, t)
	return
}

// Returns nil connection and nil error if no connection could be established
// for valid reasons.
func (cl *Client) establishOutgoingConnEx(t *torrent, addr IpPort, obfuscatedHeader bool) (*connection, error) {
	dialCtx, cancel := context.WithTimeout(context.Background(), func() time.Duration {
		cl.rLock()
		defer cl.rUnlock()
		return t.dialTimeout()
	}())
	defer cancel()
	dr := cl.dialFirst(dialCtx, addr.String())
	nc := dr.Conn
	if nc == nil {
		if dialCtx.Err() != nil {
			return nil, xerrors.Errorf("dialing: %w", dialCtx.Err())
		}
		return nil, errors.New("dial failed")
	}
	c, err := cl.handshakesConnection(context.Background(), nc, t, obfuscatedHeader, addr, dr.Network)
	if err != nil {
		nc.Close()
	}
	return c, err
}

// Returns nil connection and nil error if no connection could be established
// for valid reasons.
func (cl *Client) establishOutgoingConn(t *torrent, addr IpPort) (c *connection, err error) {
	metrics.Add("establish outgoing connection", 1)
	obfuscatedHeaderFirst := cl.config.HeaderObfuscationPolicy.Preferred
	c, err = cl.establishOutgoingConnEx(t, addr, obfuscatedHeaderFirst)
	if err == nil {
		metrics.Add("initiated conn with preferred header obfuscation", 1)
		return
	}
	//cl.logger.Printf("error establishing connection to %s (obfuscatedHeader=%t): %v", addr, obfuscatedHeaderFirst, err)
	if cl.config.HeaderObfuscationPolicy.RequirePreferred {
		// We should have just tried with the preferred header obfuscation. If it was required,
		// there's nothing else to try.
		return
	}
	// Try again with encryption if we didn't earlier, or without if we did.
	c, err = cl.establishOutgoingConnEx(t, addr, !obfuscatedHeaderFirst)
	if err == nil {
		metrics.Add("initiated conn with fallback header obfuscation", 1)
	}
	//cl.logger.Printf("error establishing fallback connection to %v: %v", addr, err)
	return
}

// Called to dial out and run a connection. The addr we're given is already
// considered half-open.
func (cl *Client) outgoingConnection(t *torrent, addr IpPort, ps peerSource, trusted bool) {
	cl.dialRateLimiter.Wait(context.Background())
	c, err := cl.establishOutgoingConn(t, addr)
	cl.lock()
	// Don't release lock between here and addConnection, unless it's for
	// failure.
	cl.noLongerHalfOpen(t, addr.String())
	cl.unlock()

	if err != nil {
		if cl.config.Debug {
			cl.logger.Printf("error establishing outgoing connection to %v: %v", addr, err)
		}
		return
	}

	c.Discovery = ps
	c.trusted = trusted

	cl.runHandshookConn(c, t)
	t.deleteConnection(c)
	t.event.Broadcast()
	cl.event.Broadcast()
}

// The port number for incoming peer connections. 0 if the client isn't
// listening.
func (cl *Client) incomingPeerPort() int {
	return cl.LocalPort()
}

func (cl *Client) initiateHandshakes(c *connection, t *torrent) error {
	if c.headerEncrypted {
		var rw io.ReadWriter
		var err error
		rw, c.cryptoMethod, err = mse.InitiateHandshake(
			struct {
				io.Reader
				io.Writer
			}{c.r, c.w},
			t.infoHash[:],
			nil,
			cl.config.CryptoProvides,
		)
		c.setRW(rw)
		if err != nil {
			return xerrors.Errorf("header obfuscation handshake: %w", err)
		}
	}
	ih, err := cl.connBtHandshake(c, &t.infoHash)
	if err != nil {
		return xerrors.Errorf("bittorrent protocol handshake: %w", err)
	}
	if ih != t.infoHash {
		return errors.New("bittorrent protocol handshake: peer infohash didn't match")
	}
	return nil
}

// Calls f with any secret keys.
func (cl *Client) forSkeys(f func([]byte) bool) {
	cl.lock()
	defer cl.unlock()
	if false { // Emulate the bug from #114
		var firstIh InfoHash
		for ih := range cl.torrents {
			firstIh = ih
			break
		}
		for range cl.torrents {
			if !f(firstIh[:]) {
				break
			}
		}
		return
	}
	for ih := range cl.torrents {
		if !f(ih[:]) {
			break
		}
	}
}

// Do encryption and bittorrent handshakes as receiver.
func (cl *Client) receiveHandshakes(c *connection) (t *torrent, err error) {
	defer perf.ScopeTimerErr(&err)()
	var rw io.ReadWriter
	rw, c.headerEncrypted, c.cryptoMethod, err = handleEncryption(c.rw(), cl.forSkeys, cl.config.HeaderObfuscationPolicy, cl.config.CryptoSelector)
	c.setRW(rw)
	if err == nil || err == mse.ErrNoSecretKeyMatch {
		if c.headerEncrypted {
			metrics.Add("handshakes received encrypted", 1)
		} else {
			metrics.Add("handshakes received unencrypted", 1)
		}
	} else {
		metrics.Add("handshakes received with error while handling encryption", 1)
	}
	if err != nil {
		if err == mse.ErrNoSecretKeyMatch {
			err = nil
		}
		return
	}
	if cl.config.HeaderObfuscationPolicy.RequirePreferred && c.headerEncrypted != cl.config.HeaderObfuscationPolicy.Preferred {
		err = errors.New("connection not have required header obfuscation")
		return
	}
	ih, err := cl.connBtHandshake(c, nil)
	if err != nil {
		err = xerrors.Errorf("during bt handshake: %w", err)
		return
	}
	cl.lock()
	t = cl.torrents[ih]
	cl.unlock()
	return
}

func (cl *Client) connBtHandshake(c *connection, ih *metainfo.Hash) (ret metainfo.Hash, err error) {
	res, err := pp.Handshake(c.rw(), ih, cl.peerID, cl.extensionBytes)
	if err != nil {
		return
	}
	ret = res.Hash
	c.PeerExtensionBytes = res.PeerExtensionBits
	c.PeerID = res.PeerID
	c.completedHandshake = time.Now()
	return
}

func (cl *Client) runReceivedConn(c *connection) {
	err := c.conn.SetDeadline(time.Now().Add(cl.config.HandshakesTimeout))
	if err != nil {
		panic(err)
	}
	t, err := cl.receiveHandshakes(c)
	if err != nil {
		log.Fmsg(
			"error receiving handshakes: %s", err,
		).AddValue(
			debugLogValue,
		).Add(
			"network", c.network,
		).Log(cl.logger)
		metrics.Add("error receiving handshake", 1)
		cl.lock()
		cl.onBadAccept(c.remoteAddr)
		cl.unlock()
		return
	}
	if t == nil {
		metrics.Add("received handshake for unloaded torrent", 1)
		log.Fmsg("received handshake for unloaded torrent").AddValue(debugLogValue).Log(cl.logger)
		cl.lock()
		cl.onBadAccept(c.remoteAddr)
		cl.unlock()
		return
	}

	metrics.Add("received handshake for loaded torrent", 1)
	cl.runHandshookConn(c, t)
}

func (cl *Client) runHandshookConn(c *connection, t *torrent) {
	c.setTorrent(t)
	if c.PeerID == cl.peerID {
		if c.outgoing {
			connsToSelf.Add(1)
			addr := c.conn.RemoteAddr().String()
			cl.dopplegangerAddrs[addr] = struct{}{}
		} else {
			// Because the remote address is not necessarily the same as its
			// client's torrent listen address, we won't record the remote address
			// as a doppleganger. Instead, the initiator can record *us* as the
			// doppleganger.
		}
		return
	}
	c.conn.SetWriteDeadline(time.Time{})
	c.r = deadlineReader{c.conn, c.r}
	completedHandshakeConnectionFlags.Add(c.connectionFlags(), 1)
	if connIsIpv6(c.conn) {
		metrics.Add("completed handshake over ipv6", 1)
	}
	if err := t.addConnection(c); err != nil {
		log.Fmsg("error adding connection: %s", err).AddValues(c, debugLogValue).Log(t.logger)
		return
	}

	defer t.event.Broadcast()
	defer t.dropConnection(c)
	c.sendInitialMessages(cl, t)
	go c.writer(time.Minute)
	if err := c.mainReadLoop(); err != nil {
		var (
			b banned
		)

		if errors.As(err, &b) {
			cl.banPeerIP(b.IP)
		}

		t.dropConnection(c)

		if cl.config.Debug {
			cl.logger.Printf("error during connection main read loop: %v", err)
		}
	}
}

func (cl *Client) dhtPort() (ret uint16) {
	cl.eachDhtServer(func(s *dht.Server) {
		ret = uint16(missinggo.AddrPort(s.Addr()))
	})
	return
}

func (cl *Client) haveDhtServer() (ret bool) {
	cl.eachDhtServer(func(_ *dht.Server) {
		ret = true
	})
	return
}

func (cl *Client) badPeerIPPort(ip net.IP, port int) bool {
	if port == 0 {
		return true
	}
	if cl.dopplegangerAddr(net.JoinHostPort(ip.String(), strconv.FormatInt(int64(port), 10))) {
		return true
	}
	if _, ok := cl.ipBlockRange(ip); ok {
		return true
	}
	if _, ok := cl.badPeerIPs[ip.String()]; ok {
		return true
	}
	return false
}

// Return a Torrent ready for insertion into a Client.
func (cl *Client) newTorrent(src Metadata) (t *torrent) {
	// use provided storage, if provided
	storageClient := cl.defaultStorage
	if src.Storage != nil {
		storageClient = storage.NewClient(src.Storage)
	}

	csize := src.ChunkSize
	if csize == 0 {
		csize = defaultChunkSize
	}

	m := &stdsync.RWMutex{}

	t = &torrent{
		displayName: src.DisplayName,
		infoHash:    src.InfoHash,
		cln:         cl,
		config:      cl.config,
		_mu:         m,
		// metadataChanged: sync.Cond{
		// 	L: m,
		// },
		// event: &events, // for now pass through the event sync.Cond
		// storageLock: newDebugLock(&stdsync.RWMutex{}),
		peers: prioritizedPeers{
			om: btree.New(32),
			getPrio: func(p Peer) peerPriority {
				return bep40PriorityIgnoreError(cl.publicAddr(p.IP), p.addr())
			},
		},
		conns: make(map[*connection]struct{}, 2*cl.config.EstablishedConnsPerTorrent),

		halfOpen:          make(map[string]Peer),
		pieceStateChanges: pubsub.NewPubSub(),

		storageOpener:       storageClient,
		maxEstablishedConns: cl.config.EstablishedConnsPerTorrent,

		networkingEnabled:       true,
		duplicateRequestTimeout: 2 * time.Minute,

		piecesM: newChunks(csize, &metainfo.Info{}),
	}

	t.digests = newDigests(
		func(idx int) *Piece { return t.piece(idx) },
		func(idx int, cause error) {
			if err := t.pieceHashed(idx, cause); err != nil {
				t.logger.Print(err)
			}
			t.updatePieceCompletion(idx)
			t.publishPieceChange(idx)
			t.updatePiecePriority(idx)
		},
	)
	t.metadataChanged = sync.Cond{L: tlocker{torrent: t}}
	t.event = &sync.Cond{L: tlocker{torrent: t}}
	t.logger = cl.logger.WithValues(t).WithText(func(m log.Msg) string {
		return fmt.Sprintf("%v: %s", t, m.Text())
	})

	t.setChunkSize(pp.Integer(csize))
	t.setInfoBytes(src.InfoBytes)
	t.addTrackers(src.Trackers)
	cl.AddDHTNodes(src.Nodes)

	return t
}

// Handle a file-like handle to some torrent data resource.
type Handle interface {
	io.Reader
	io.Seeker
	io.Closer
	io.ReaderAt
}

func (cl *Client) dropTorrent(infoHash metainfo.Hash) (err error) {
	cl.lock()
	defer cl.unlock()

	t, ok := cl.torrents[infoHash]
	if !ok {
		return fmt.Errorf("no such torrent")
	}
	err = t.close()
	delete(cl.torrents, infoHash)
	return err
}

func (cl *Client) allTorrentsCompleted() bool {
	for _, t := range cl.torrents {
		if !t.haveInfo() {
			return false
		}
		if !t.haveAllPieces() {
			return false
		}
	}
	return true
}

// Returns true when all torrents are completely downloaded and false if the
// client is stopped before that.
func (cl *Client) WaitAll() bool {
	cl.lock()
	defer cl.unlock()
	for !cl.allTorrentsCompleted() {
		if cl.closed.IsSet() {
			return false
		}
		cl.event.Wait()
	}
	return true
}

// Returns handles to all the torrents loaded in the Client.
func (cl *Client) Torrents() []Torrent {
	cl.lock()
	defer cl.unlock()
	return cl.torrentsAsSlice()
}

func (cl *Client) torrentsAsSlice() (ret []Torrent) {
	for _, t := range cl.torrents {
		ret = append(ret, t)
	}
	return
}

func (cl *Client) DhtServers() []*dht.Server {
	return cl.dhtServers
}

func (cl *Client) AddDHTNodes(nodes []string) {
	for _, n := range nodes {
		hmp := missinggo.SplitHostMaybePort(n)
		ip := net.ParseIP(hmp.Host)
		if ip == nil {
			cl.logger.Printf("won't add DHT node with bad IP: %q", hmp.Host)
			continue
		}
		ni := krpc.NodeInfo{
			Addr: krpc.NodeAddr{
				IP:   ip,
				Port: hmp.Port,
			},
		}
		cl.eachDhtServer(func(s *dht.Server) {
			s.AddNode(ni)
		})
	}
}

func (cl *Client) banPeerIP(ip net.IP) {
	cl.logger.Printf("banning ip %v", ip)
	if cl.badPeerIPs == nil {
		cl.badPeerIPs = make(map[string]struct{})
	}
	cl.badPeerIPs[ip.String()] = struct{}{}
}

func (cl *Client) newConnection(nc net.Conn, outgoing bool, remoteAddr IpPort, network string) (c *connection) {
	c = &connection{
		// _mu:             newDebugLock(&stdsync.RWMutex{}),
		_mu:             &stdsync.RWMutex{},
		conn:            nc,
		outgoing:        outgoing,
		Choked:          true,
		PeerChoked:      true,
		PeerMaxRequests: 250,
		writeBuffer:     new(bytes.Buffer),
		remoteAddr:      remoteAddr,
		network:         network,
		touched:         roaring.NewBitmap(),
		requests:        make(map[uint64]request, maxRequests),
		PeerRequests:    make(map[request]struct{}, maxRequests),
		drop:            make(chan error, 1),
		writerDone:      make(chan struct{}),
	}
	c.logger = cl.logger.WithValues(c,
		log.Debug, // I want messages to default to debug, and can set it here as it's only used by new code
	).WithText(func(m log.Msg) string {
		return fmt.Sprintf("%v: %s", c, m.Text())
	})
	c.writerCond.L = c._mu
	c.setRW(connStatsReadWriter{nc, c})
	c.r = &rateLimitedReader{
		l: cl.config.DownloadRateLimiter,
		r: c.r,
	}
	c.logger.Printf("initialized with remote %v over network %v (outgoing=%t)", remoteAddr, network, outgoing)
	return
}

func (cl *Client) onDHTAnnouncePeer(ih metainfo.Hash, ip net.IP, port int, portOk bool) {
	cl.lock()
	defer cl.unlock()
	t := cl.torrent(ih)
	if t == nil {
		return
	}
	t.addPeers([]Peer{{
		IP:     ip,
		Port:   port,
		Source: peerSourceDhtAnnouncePeer,
	}})
}

func firstNotNil(ips ...net.IP) net.IP {
	for _, ip := range ips {
		if ip != nil {
			return ip
		}
	}
	return nil
}

func (cl *Client) eachListener(f func(socket) bool) {
	for _, s := range cl.conns {
		if !f(s) {
			break
		}
	}
}

func (cl *Client) findListener(f func(net.Listener) bool) (ret net.Listener) {
	cl.eachListener(func(l socket) bool {
		ret = l
		return !f(l)
	})
	return
}

func (cl *Client) publicIp(peer net.IP) net.IP {
	// TODO: Use BEP 10 to determine how peers are seeing us.
	if peer.To4() != nil {
		return firstNotNil(
			cl.config.PublicIp4,
			cl.findListenerIp(func(ip net.IP) bool { return ip.To4() != nil }),
		)
	}

	return firstNotNil(
		cl.config.PublicIp6,
		cl.findListenerIp(func(ip net.IP) bool { return ip.To4() == nil }),
	)
}

func (cl *Client) findListenerIp(f func(net.IP) bool) net.IP {
	return missinggo.AddrIP(cl.findListener(func(l net.Listener) bool {
		return f(missinggo.AddrIP(l.Addr()))
	}).Addr())
}

// Our IP as a peer should see it.
func (cl *Client) publicAddr(peer net.IP) IpPort {
	return IpPort{IP: cl.publicIp(peer), Port: uint16(cl.incomingPeerPort())}
}

// ListenAddrs addresses currently being listened to.
func (cl *Client) ListenAddrs() (ret []net.Addr) {
	cl.lock()
	defer cl.unlock()
	cl.eachListener(func(l socket) bool {
		ret = append(ret, l.Addr())
		return true
	})
	return
}

func (cl *Client) onBadAccept(addr IpPort) {
	ip := maskIpForAcceptLimiting(addr.IP)
	if cl.acceptLimiter == nil {
		cl.acceptLimiter = make(map[ipStr]int)
	}
	cl.acceptLimiter[ipStr(ip.String())]++
}

func maskIpForAcceptLimiting(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.Mask(net.CIDRMask(24, 32))
	}
	return ip
}

func (cl *Client) clearAcceptLimits() {
	cl.acceptLimiter = nil
}

func (cl *Client) acceptLimitClearer() {
	for {
		select {
		case <-cl.closed.LockedChan(cl.locker()):
			return
		case <-time.After(15 * time.Minute):
			cl.lock()
			cl.clearAcceptLimits()
			cl.unlock()
		}
	}
}

func (cl *Client) rateLimitAccept(ip net.IP) bool {
	if cl.config.DisableAcceptRateLimiting {
		return false
	}
	return cl.acceptLimiter[ipStr(maskIpForAcceptLimiting(ip).String())] > 0
}

var _ = atomic.AddInt32

func (cl *Client) rLock() {
	// updated := atomic.AddUint64(&lcount, 1)
	// l2.Output(2, fmt.Sprintf("%p rlock initiated - %d", cl, updated))
	cl._mu.RLock()
	// l2.Output(2, fmt.Sprintf("%p rlock completed - %d", cl, updated))
}

func (cl *Client) rUnlock() {
	// updated := atomic.AddUint64(&ucount, 1)
	// l2.Output(2, fmt.Sprintf("%p runlock initiated - %d", cl, updated))
	cl._mu.RUnlock()
	// l2.Output(2, fmt.Sprintf("%p runlock completed - %d", cl, updated))
}

var lcount = uint64(0)
var ucount = uint64(0)

func (cl *Client) lock() {
	// updated := atomic.AddUint64(&lcount, 1)
	// l2.Output(2, fmt.Sprintf("%p lock initiated - %d", cl, updated))
	cl._mu.Lock()
	// l2.Output(2, fmt.Sprintf("%p lock completed - %d", cl, updated))
}

func (cl *Client) unlock() {
	// updated := atomic.AddUint64(&ucount, 1)
	// l2.Output(2, fmt.Sprintf("%p unlock initiated - %d", cl, updated))
	cl._mu.Unlock()
	// l2.Output(2, fmt.Sprintf("%p unlock completed - %d", cl, updated))
}

func (cl *Client) locker() sync.Locker {
	return clientLocker{cl}
}

func (cl *Client) String() string {
	return fmt.Sprintf("<%[1]T %[1]p>", cl)
}

type clientLocker struct {
	*Client
}

func (cl clientLocker) Lock() {
	// updated := atomic.AddUint64(&lcount, 1)
	// l2.Output(2, fmt.Sprintf("%p lock initiated - %d", cl.Client, updated))
	cl._mu.Lock()
	// l2.Output(2, fmt.Sprintf("%p lock completed - %d", cl.Client, updated))
}

func (cl clientLocker) Unlock() {
	// updated := atomic.AddUint64(&ucount, 1)
	// l2.Output(2, fmt.Sprintf("%p unlock initiated - %d", cl.Client, updated))
	cl._mu.Unlock()
	// l2.Output(2, fmt.Sprintf("%p unlock completed - %d", cl.Client, updated))
}
