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
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/bitmap"
	"github.com/anacrolix/missinggo/perf"
	"github.com/anacrolix/missinggo/pproffd"
	"github.com/anacrolix/missinggo/pubsub"
	"github.com/anacrolix/missinggo/slices"
	"github.com/anacrolix/sync"
	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	"github.com/google/btree"
	"golang.org/x/time/rate"
	"golang.org/x/xerrors"

	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/conntrack"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

// Clients contain zero or more Torrents. A Client manages a blocklist, the
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
	dialers        []Dialer
	listeners      []Listener
	dhtServers     []DhtServer
	ipBlockList    iplist.Ranger
	// Our BitTorrent protocol extension bytes, sent in our BT handshakes.
	extensionBytes pp.PeerExtensionBits

	// Set of addresses that have our client ID. This intentionally will
	// include ourselves if we end up trying to connect to our own address
	// through legitimate channels.
	dopplegangerAddrs map[string]struct{}
	badPeerIPs        map[string]struct{}
	torrents          map[InfoHash]*Torrent

	acceptLimiter   map[ipStr]int
	dialRateLimiter *rate.Limiter
}

type ipStr string

func (cl *Client) BadPeerIPs() []string {
	cl.rLock()
	defer cl.rUnlock()
	return cl.badPeerIPsLocked()
}

func (cl *Client) badPeerIPsLocked() []string {
	return slices.FromMapKeys(cl.badPeerIPs).([]string)
}

func (cl *Client) PeerID() PeerID {
	return cl.peerID
}

// Returns the port number for the first listener that has one. No longer assumes that all port
// numbers are the same, due to support for custom listeners. Returns zero if no port number is
// found.
func (cl *Client) LocalPort() (port int) {
	cl.eachListener(func(l Listener) bool {
		port = addrPortOrZero(l.Addr())
		return port == 0
	})
	return
}

func writeDhtServerStatus(w io.Writer, s DhtServer) {
	dhtStats := s.Stats()
	fmt.Fprintf(w, " ID: %x\n", s.ID())
	spew.Fdump(w, dhtStats)
}

// Writes out a human readable status of the client, such as for writing to a
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
	cl.eachDhtServer(func(s DhtServer) {
		fmt.Fprintf(w, "%s DHT server at %s:\n", s.Addr().Network(), s.Addr().String())
		writeDhtServerStatus(w, s)
	})
	spew.Fdump(w, &cl.stats)
	fmt.Fprintf(w, "# Torrents: %d\n", len(cl.torrentsAsSlice()))
	fmt.Fprintln(w)
	for _, t := range slices.Sort(cl.torrentsAsSlice(), func(l, r *Torrent) bool {
		return l.InfoHash().AsString() < r.InfoHash().AsString()
	}).([]*Torrent) {
		if t.name() == "" {
			fmt.Fprint(w, "<unknown name>")
		} else {
			fmt.Fprint(w, t.name())
		}
		fmt.Fprint(w, "\n")
		if t.info != nil {
			fmt.Fprintf(w, "%f%% of %d bytes (%s)", 100*(1-float64(t.bytesMissingLocked())/float64(t.info.TotalLength())), t.length, humanize.Bytes(uint64(t.info.TotalLength())))
		} else {
			w.WriteString("<missing metainfo>")
		}
		fmt.Fprint(w, "\n")
		t.writeStatus(w)
		fmt.Fprintln(w)
	}
}

var debugLogValue = log.Debug

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
		torrents:          make(map[metainfo.Hash]*Torrent),
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
		// We'd use mmap by default but HFS+ doesn't support sparse files.
		storageImplCloser := storage.NewFile(cfg.DataDir)
		cl.onClose = append(cl.onClose, func() {
			if err := storageImplCloser.Close(); err != nil {
				cl.logger.Printf("error closing default storage: %s", err)
			}
		})
		storageImpl = storageImplCloser
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

	sockets, err := listenAll(cl.listenNetworks(), cl.config.ListenHost, cl.config.ListenPort, cl.firewallCallback)
	if err != nil {
		return
	}

	// Check for panics.
	cl.LocalPort()

	for _, _s := range sockets {
		s := _s // Go is fucking retarded.
		cl.onClose = append(cl.onClose, func() { s.Close() })
		if peerNetworkEnabled(parseNetworkString(s.Addr().Network()), cl.config) {
			cl.dialers = append(cl.dialers, s)
			cl.listeners = append(cl.listeners, s)
			go cl.acceptConnections(s)
		}
	}

	go cl.forwardPort()
	if !cfg.NoDHT {
		for _, s := range sockets {
			if pc, ok := s.(net.PacketConn); ok {
				ds, err := cl.newAnacrolixDhtServer(pc)
				if err != nil {
					panic(err)
				}
				cl.dhtServers = append(cl.dhtServers, anacrolixDhtServerWrapper{ds})
				cl.onClose = append(cl.onClose, func() { ds.Close() })
			}
		}
	}

	return
}

func (cl *Client) AddDhtServer(d DhtServer) {
	cl.dhtServers = append(cl.dhtServers, d)
}

// Adds a Dialer for outgoing connections. All Dialers are used when attempting to connect to a
// given address for any Torrent.
func (cl *Client) AddDialer(d Dialer) {
	cl.dialers = append(cl.dialers, d)
}

// Registers a Listener, and starts Accepting on it. You must Close Listeners provided this way
// yourself.
func (cl *Client) AddListener(l Listener) {
	cl.listeners = append(cl.listeners, l)
	go cl.acceptConnections(l)
}

func (cl *Client) firewallCallback(net.Addr) bool {
	cl.rLock()
	block := !cl.wantConns()
	cl.rUnlock()
	if block {
		torrent.Add("connections firewalled", 1)
	} else {
		torrent.Add("connections not firewalled", 1)
	}
	return block
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

func (cl *Client) newAnacrolixDhtServer(conn net.PacketConn) (s *dht.Server, err error) {
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
		StartingNodes:      cl.config.DhtStartingNodes(conn.LocalAddr().Network()),
		ConnectionTracking: cl.config.ConnTracker,
		OnQuery:            cl.config.DHTOnQuery,
		Logger: cl.logger.WithText(func(m log.Msg) string {
			return fmt.Sprintf("dht server on %v: %s", conn.LocalAddr().String(), m.Text())
		}),
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

func (cl *Client) eachDhtServer(f func(DhtServer)) {
	for _, ds := range cl.dhtServers {
		f(ds)
	}
}

// Stops the client. All connections to peers are closed and all activity will
// come to a halt.
func (cl *Client) Close() {
	cl.lock()
	defer cl.unlock()
	cl.closed.Set()
	for _, t := range cl.torrents {
		t.close()
	}
	for i := range cl.onClose {
		cl.onClose[len(cl.onClose)-1-i]()
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

// TODO: Apply filters for non-standard networks, particularly rate-limiting.
func (cl *Client) rejectAccepted(conn net.Conn) error {
	ra := conn.RemoteAddr()
	if rip := addrIpOrNil(ra); rip != nil {
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
	}
	return nil
}

func (cl *Client) acceptConnections(l net.Listener) {
	for {
		conn, err := l.Accept()
		torrent.Add("client listener accepts", 1)
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
				torrent.Add("rejected accepted connections", 1)
				log.Fmsg("rejecting accepted conn: %v", reject).AddValue(debugLogValue).Log(cl.logger)
				conn.Close()
			} else {
				go cl.incomingConnection(conn)
			}
			log.Fmsg("accepted %q connection at %q from %q",
				l.Addr().Network(),
				conn.LocalAddr(),
				conn.RemoteAddr(),
			).AddValue(debugLogValue).Log(cl.logger)
			torrent.Add(fmt.Sprintf("accepted conn remote IP len=%d", len(addrIpOrNil(conn.RemoteAddr()))), 1)
			torrent.Add(fmt.Sprintf("accepted conn network=%s", conn.RemoteAddr().Network()), 1)
			torrent.Add(fmt.Sprintf("accepted on %s listener", l.Addr().Network()), 1)
		}()
	}
}

func (cl *Client) incomingConnection(nc net.Conn) {
	defer nc.Close()
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c := cl.newConnection(nc, false, nc.RemoteAddr(), nc.RemoteAddr().Network())
	c.Discovery = PeerSourceIncoming
	cl.runReceivedConn(c)
}

// Returns a handle to the given torrent, if it's present in the client.
func (cl *Client) Torrent(ih metainfo.Hash) (t *Torrent, ok bool) {
	cl.lock()
	defer cl.unlock()
	t, ok = cl.torrents[ih]
	return
}

func (cl *Client) torrent(ih metainfo.Hash) *Torrent {
	return cl.torrents[ih]
}

type dialResult struct {
	Conn    net.Conn
	Network string
}

func countDialResult(err error) {
	if err == nil {
		torrent.Add("successful dials", 1)
	} else {
		torrent.Add("unsuccessful dials", 1)
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
		cl.eachDialer(func(s Dialer) bool {
			func() {
				left++
				//cl.logger.Printf("dialing %s on %s/%s", addr, s.Addr().Network(), s.Addr())
				go func() {
					resCh <- dialResult{
						cl.dialFromSocket(ctx, s, addr),
						s.LocalAddr().Network(),
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
		go torrent.Add(fmt.Sprintf("network dialed first: %s", res.Conn.RemoteAddr().Network()), 1)
	}
	//if res.Conn != nil {
	//	cl.logger.Printf("first connection for %s from %s/%s", addr, res.Conn.LocalAddr().Network(), res.Conn.LocalAddr().String())
	//} else {
	//	cl.logger.Printf("failed to dial %s", addr)
	//}
	return res
}

func (cl *Client) dialFromSocket(ctx context.Context, s Dialer, addr string) net.Conn {
	network := s.LocalAddr().Network()
	cte := cl.config.ConnTracker.Wait(
		ctx,
		conntrack.Entry{network, s.LocalAddr().String(), addr},
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
	c, err := s.Dial(ctx, addr)
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

func (cl *Client) noLongerHalfOpen(t *Torrent, addr string) {
	if _, ok := t.halfOpen[addr]; !ok {
		panic("invariant broken")
	}
	delete(t.halfOpen, addr)
	t.openNewConns()
}

// Performs initiator handshakes and returns a connection. Returns nil
// *connection if no connection for valid reasons.
func (cl *Client) handshakesConnection(ctx context.Context, nc net.Conn, t *Torrent, encryptHeader bool, remoteAddr net.Addr, network string) (c *PeerConn, err error) {
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
func (cl *Client) establishOutgoingConnEx(t *Torrent, addr net.Addr, obfuscatedHeader bool) (*PeerConn, error) {
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
func (cl *Client) establishOutgoingConn(t *Torrent, addr net.Addr) (c *PeerConn, err error) {
	torrent.Add("establish outgoing connection", 1)
	obfuscatedHeaderFirst := cl.config.HeaderObfuscationPolicy.Preferred
	c, err = cl.establishOutgoingConnEx(t, addr, obfuscatedHeaderFirst)
	if err == nil {
		torrent.Add("initiated conn with preferred header obfuscation", 1)
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
		torrent.Add("initiated conn with fallback header obfuscation", 1)
	}
	//cl.logger.Printf("error establishing fallback connection to %v: %v", addr, err)
	return
}

// Called to dial out and run a connection. The addr we're given is already
// considered half-open.
func (cl *Client) outgoingConnection(t *Torrent, addr net.Addr, ps PeerSource, trusted bool) {
	cl.dialRateLimiter.Wait(context.Background())
	c, err := cl.establishOutgoingConn(t, addr)
	cl.lock()
	defer cl.unlock()
	// Don't release lock between here and addConnection, unless it's for
	// failure.
	cl.noLongerHalfOpen(t, addr.String())
	if err != nil {
		if cl.config.Debug {
			cl.logger.Printf("error establishing outgoing connection to %v: %v", addr, err)
		}
		return
	}
	defer c.close()
	c.Discovery = ps
	c.trusted = trusted
	cl.runHandshookConn(c, t)
}

// The port number for incoming peer connections. 0 if the client isn't listening.
func (cl *Client) incomingPeerPort() int {
	return cl.LocalPort()
}

func (cl *Client) initiateHandshakes(c *PeerConn, t *Torrent) error {
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
func (cl *Client) receiveHandshakes(c *PeerConn) (t *Torrent, err error) {
	defer perf.ScopeTimerErr(&err)()
	var rw io.ReadWriter
	rw, c.headerEncrypted, c.cryptoMethod, err = handleEncryption(c.rw(), cl.forSkeys, cl.config.HeaderObfuscationPolicy, cl.config.CryptoSelector)
	c.setRW(rw)
	if err == nil || err == mse.ErrNoSecretKeyMatch {
		if c.headerEncrypted {
			torrent.Add("handshakes received encrypted", 1)
		} else {
			torrent.Add("handshakes received unencrypted", 1)
		}
	} else {
		torrent.Add("handshakes received with error while handling encryption", 1)
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

func (cl *Client) connBtHandshake(c *PeerConn, ih *metainfo.Hash) (ret metainfo.Hash, err error) {
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

func (cl *Client) runReceivedConn(c *PeerConn) {
	err := c.conn.SetDeadline(time.Now().Add(cl.config.HandshakesTimeout))
	if err != nil {
		panic(err)
	}
	t, err := cl.receiveHandshakes(c)
	if err != nil {
		log.Fmsg(
			"error receiving handshakes on %v: %s", c, err,
		).AddValue(
			debugLogValue,
		).Add(
			"network", c.network,
		).Log(cl.logger)
		torrent.Add("error receiving handshake", 1)
		cl.lock()
		cl.onBadAccept(c.remoteAddr)
		cl.unlock()
		return
	}
	if t == nil {
		torrent.Add("received handshake for unloaded torrent", 1)
		log.Fmsg("received handshake for unloaded torrent").AddValue(debugLogValue).Log(cl.logger)
		cl.lock()
		cl.onBadAccept(c.remoteAddr)
		cl.unlock()
		return
	}
	torrent.Add("received handshake for loaded torrent", 1)
	cl.lock()
	defer cl.unlock()
	cl.runHandshookConn(c, t)
}

func (cl *Client) runHandshookConn(c *PeerConn, t *Torrent) {
	c.setTorrent(t)
	if c.PeerID == cl.peerID {
		if c.outgoing {
			connsToSelf.Add(1)
			addr := c.conn.RemoteAddr().String()
			cl.dopplegangerAddrs[addr] = struct{}{}
		} else {
			// Because the remote address is not necessarily the same as its client's torrent listen
			// address, we won't record the remote address as a doppleganger. Instead, the initiator
			// can record *us* as the doppleganger.
		}
		return
	}
	c.conn.SetWriteDeadline(time.Time{})
	c.r = deadlineReader{c.conn, c.r}
	completedHandshakeConnectionFlags.Add(c.connectionFlags(), 1)
	if connIsIpv6(c.conn) {
		torrent.Add("completed handshake over ipv6", 1)
	}
	if err := t.addConnection(c); err != nil {
		log.Fmsg("error adding connection: %s", err).AddValues(c, debugLogValue).Log(t.logger)
		return
	}
	defer t.dropConnection(c)
	go c.writer(time.Minute)
	cl.sendInitialMessages(c, t)
	err := c.mainReadLoop()
	if err != nil && cl.config.Debug {
		cl.logger.Printf("error during connection main read loop: %s", err)
	}
}

// See the order given in Transmission's tr_peerMsgsNew.
func (cl *Client) sendInitialMessages(conn *PeerConn, torrent *Torrent) {
	if conn.PeerExtensionBytes.SupportsExtended() && cl.extensionBytes.SupportsExtended() {
		conn.post(pp.Message{
			Type:       pp.Extended,
			ExtendedID: pp.HandshakeExtendedID,
			ExtendedPayload: func() []byte {
				msg := pp.ExtendedHandshakeMessage{
					M: map[pp.ExtensionName]pp.ExtensionNumber{
						pp.ExtensionNameMetadata: metadataExtendedId,
					},
					V:            cl.config.ExtendedHandshakeClientVersion,
					Reqq:         64, // TODO: Really?
					YourIp:       pp.CompactIp(addrIpOrNil(conn.remoteAddr)),
					Encryption:   cl.config.HeaderObfuscationPolicy.Preferred || !cl.config.HeaderObfuscationPolicy.RequirePreferred,
					Port:         cl.incomingPeerPort(),
					MetadataSize: torrent.metadataSize(),
					// TODO: We can figured these out specific to the socket
					// used.
					Ipv4: pp.CompactIp(cl.config.PublicIp4.To4()),
					Ipv6: cl.config.PublicIp6.To16(),
				}
				if !cl.config.DisablePEX {
					msg.M[pp.ExtensionNamePex] = pexExtendedId
				}
				return bencode.MustMarshal(msg)
			}(),
		})
	}
	func() {
		if conn.fastEnabled() {
			if torrent.haveAllPieces() {
				conn.post(pp.Message{Type: pp.HaveAll})
				conn.sentHaves.AddRange(0, bitmap.BitIndex(conn.t.NumPieces()))
				return
			} else if !torrent.haveAnyPieces() {
				conn.post(pp.Message{Type: pp.HaveNone})
				conn.sentHaves.Clear()
				return
			}
		}
		conn.postBitfield()
	}()
	if conn.PeerExtensionBytes.SupportsDHT() && cl.extensionBytes.SupportsDHT() && cl.haveDhtServer() {
		conn.post(pp.Message{
			Type: pp.Port,
			Port: cl.dhtPort(),
		})
	}
}

func (cl *Client) dhtPort() (ret uint16) {
	cl.eachDhtServer(func(s DhtServer) {
		ret = uint16(missinggo.AddrPort(s.Addr()))
	})
	return
}

func (cl *Client) haveDhtServer() (ret bool) {
	cl.eachDhtServer(func(_ DhtServer) {
		ret = true
	})
	return
}

// Process incoming ut_metadata message.
func (cl *Client) gotMetadataExtensionMsg(payload []byte, t *Torrent, c *PeerConn) error {
	var d map[string]int
	err := bencode.Unmarshal(payload, &d)
	if _, ok := err.(bencode.ErrUnusedTrailingBytes); ok {
	} else if err != nil {
		return fmt.Errorf("error unmarshalling bencode: %s", err)
	}
	msgType, ok := d["msg_type"]
	if !ok {
		return errors.New("missing msg_type field")
	}
	piece := d["piece"]
	switch msgType {
	case pp.DataMetadataExtensionMsgType:
		c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.MetadataChunksRead }))
		if !c.requestedMetadataPiece(piece) {
			return fmt.Errorf("got unexpected piece %d", piece)
		}
		c.metadataRequests[piece] = false
		begin := len(payload) - metadataPieceSize(d["total_size"], piece)
		if begin < 0 || begin >= len(payload) {
			return fmt.Errorf("data has bad offset in payload: %d", begin)
		}
		t.saveMetadataPiece(piece, payload[begin:])
		c.lastUsefulChunkReceived = time.Now()
		return t.maybeCompleteMetadata()
	case pp.RequestMetadataExtensionMsgType:
		if !t.haveMetadataPiece(piece) {
			c.post(t.newMetadataExtensionMessage(c, pp.RejectMetadataExtensionMsgType, d["piece"], nil))
			return nil
		}
		start := (1 << 14) * piece
		c.logger.Printf("sending metadata piece %d", piece)
		c.post(t.newMetadataExtensionMessage(c, pp.DataMetadataExtensionMsgType, piece, t.metadataBytes[start:start+t.metadataPieceSize(piece)]))
		return nil
	case pp.RejectMetadataExtensionMsgType:
		return nil
	default:
		return errors.New("unknown msg_type value")
	}
}

func (cl *Client) badPeerAddr(addr net.Addr) bool {
	if ipa, ok := tryIpPortFromNetAddr(addr); ok {
		return cl.badPeerIPPort(ipa.IP, ipa.Port)
	}
	return false
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
func (cl *Client) newTorrent(ih metainfo.Hash, specStorage storage.ClientImpl) (t *Torrent) {
	// use provided storage, if provided
	storageClient := cl.defaultStorage
	if specStorage != nil {
		storageClient = storage.NewClient(specStorage)
	}

	t = &Torrent{
		cl:       cl,
		infoHash: ih,
		peers: prioritizedPeers{
			om: btree.New(32),
			getPrio: func(p Peer) peerPriority {
				return bep40PriorityIgnoreError(cl.publicAddr(addrIpOrNil(p.Addr)), p.addr())
			},
		},
		conns: make(map[*PeerConn]struct{}, 2*cl.config.EstablishedConnsPerTorrent),

		halfOpen:          make(map[string]Peer),
		pieceStateChanges: pubsub.NewPubSub(),

		storageOpener:       storageClient,
		maxEstablishedConns: cl.config.EstablishedConnsPerTorrent,

		networkingEnabled: true,
		metadataChanged: sync.Cond{
			L: cl.locker(),
		},
	}
	t._pendingPieces.NewSet = priorityBitmapStableNewSet
	t.requestStrategy = cl.config.DefaultRequestStrategy(t.requestStrategyCallbacks(), &cl._mu)
	t.logger = cl.logger.WithValues(t).WithText(func(m log.Msg) string {
		return fmt.Sprintf("%v: %s", t, m.Text())
	})
	t.setChunkSize(defaultChunkSize)
	return
}

// A file-like handle to some torrent data resource.
type Handle interface {
	io.Reader
	io.Seeker
	io.Closer
	io.ReaderAt
}

func (cl *Client) AddTorrentInfoHash(infoHash metainfo.Hash) (t *Torrent, new bool) {
	return cl.AddTorrentInfoHashWithStorage(infoHash, nil)
}

// Adds a torrent by InfoHash with a custom Storage implementation.
// If the torrent already exists then this Storage is ignored and the
// existing torrent returned with `new` set to `false`
func (cl *Client) AddTorrentInfoHashWithStorage(infoHash metainfo.Hash, specStorage storage.ClientImpl) (t *Torrent, new bool) {
	cl.lock()
	defer cl.unlock()
	t, ok := cl.torrents[infoHash]
	if ok {
		return
	}
	new = true

	t = cl.newTorrent(infoHash, specStorage)
	cl.eachDhtServer(func(s DhtServer) {
		go t.dhtAnnouncer(s)
	})
	cl.torrents[infoHash] = t
	cl.clearAcceptLimits()
	t.updateWantPeersEvent()
	// Tickle Client.waitAccept, new torrent may want conns.
	cl.event.Broadcast()
	return
}

// Add or merge a torrent spec. If the torrent is already present, the
// trackers will be merged with the existing ones. If the Info isn't yet
// known, it will be set. The display name is replaced if the new spec
// provides one. Returns new if the torrent wasn't already in the client.
// Note that any `Storage` defined on the spec will be ignored if the
// torrent is already present (i.e. `new` return value is `true`)
func (cl *Client) AddTorrentSpec(spec *TorrentSpec) (t *Torrent, new bool, err error) {
	t, new = cl.AddTorrentInfoHashWithStorage(spec.InfoHash, spec.Storage)
	if spec.DisplayName != "" {
		t.SetDisplayName(spec.DisplayName)
	}
	if spec.InfoBytes != nil {
		err = t.SetInfoBytes(spec.InfoBytes)
		if err != nil {
			return
		}
	}
	cl.lock()
	defer cl.unlock()
	if spec.ChunkSize != 0 {
		t.setChunkSize(pp.Integer(spec.ChunkSize))
	}
	t.addTrackers(spec.Trackers)
	t.maybeNewConns()
	return
}

func (cl *Client) dropTorrent(infoHash metainfo.Hash) (err error) {
	t, ok := cl.torrents[infoHash]
	if !ok {
		err = fmt.Errorf("no such torrent")
		return
	}
	err = t.close()
	if err != nil {
		panic(err)
	}
	delete(cl.torrents, infoHash)
	return
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
func (cl *Client) Torrents() []*Torrent {
	cl.lock()
	defer cl.unlock()
	return cl.torrentsAsSlice()
}

func (cl *Client) torrentsAsSlice() (ret []*Torrent) {
	for _, t := range cl.torrents {
		ret = append(ret, t)
	}
	return
}

func (cl *Client) AddMagnet(uri string) (T *Torrent, err error) {
	spec, err := TorrentSpecFromMagnetURI(uri)
	if err != nil {
		return
	}
	T, _, err = cl.AddTorrentSpec(spec)
	return
}

func (cl *Client) AddTorrent(mi *metainfo.MetaInfo) (T *Torrent, err error) {
	T, _, err = cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	var ss []string
	slices.MakeInto(&ss, mi.Nodes)
	cl.AddDHTNodes(ss)
	return
}

func (cl *Client) AddTorrentFromFile(filename string) (T *Torrent, err error) {
	mi, err := metainfo.LoadFromFile(filename)
	if err != nil {
		return
	}
	return cl.AddTorrent(mi)
}

func (cl *Client) DhtServers() []DhtServer {
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
		cl.eachDhtServer(func(s DhtServer) {
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

func (cl *Client) newConnection(nc net.Conn, outgoing bool, remoteAddr net.Addr, network string) (c *PeerConn) {
	c = &PeerConn{
		conn:            nc,
		outgoing:        outgoing,
		choking:         true,
		peerChoking:     true,
		PeerMaxRequests: 250,
		writeBuffer:     new(bytes.Buffer),
		remoteAddr:      remoteAddr,
		network:         network,
	}
	c.logger = cl.logger.WithValues(c,
		log.Debug, // I want messages to default to debug, and can set it here as it's only used by new code
	).WithText(func(m log.Msg) string {
		return fmt.Sprintf("%v: %s", c, m.Text())
	})
	c.writerCond.L = cl.locker()
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
		Addr:   ipPortAddr{ip, port},
		Source: PeerSourceDhtAnnouncePeer,
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

func (cl *Client) eachDialer(f func(Dialer) bool) {
	for _, s := range cl.dialers {
		if !f(s) {
			break
		}
	}
}

func (cl *Client) eachListener(f func(Listener) bool) {
	for _, s := range cl.listeners {
		if !f(s) {
			break
		}
	}
}

func (cl *Client) findListener(f func(net.Listener) bool) (ret net.Listener) {
	cl.eachListener(func(l Listener) bool {
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
	return addrIpOrNil(
		cl.findListener(
			func(l net.Listener) bool {
				return f(addrIpOrNil(l.Addr()))
			},
		).Addr(),
	)
}

// Our IP as a peer should see it.
func (cl *Client) publicAddr(peer net.IP) IpPort {
	return IpPort{IP: cl.publicIp(peer), Port: uint16(cl.incomingPeerPort())}
}

// ListenAddrs addresses currently being listened to.
func (cl *Client) ListenAddrs() (ret []net.Addr) {
	cl.lock()
	defer cl.unlock()
	cl.eachListener(func(l Listener) bool {
		ret = append(ret, l.Addr())
		return true
	})
	return
}

func (cl *Client) onBadAccept(addr net.Addr) {
	ipa, ok := tryIpPortFromNetAddr(addr)
	if !ok {
		return
	}
	ip := maskIpForAcceptLimiting(ipa.IP)
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

func (cl *Client) rLock() {
	cl._mu.RLock()
}

func (cl *Client) rUnlock() {
	cl._mu.RUnlock()
}

func (cl *Client) lock() {
	cl._mu.Lock()
}

func (cl *Client) unlock() {
	cl._mu.Unlock()
}

func (cl *Client) locker() *lockWithDeferreds {
	return &cl._mu
}

func (cl *Client) String() string {
	return fmt.Sprintf("<%[1]T %[1]p>", cl)
}
