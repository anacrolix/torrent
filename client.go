package torrent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"time"

	"github.com/james-lawrence/torrent/connections"

	"github.com/anacrolix/missinggo/slices"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/sync"
	"github.com/davecgh/go-spew/spew"
	"github.com/james-lawrence/torrent/dht/v2"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
	"github.com/pkg/errors"
	"golang.org/x/time/rate"
	"golang.org/x/xerrors"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/x/stringsx"
	"github.com/james-lawrence/torrent/iplist"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/mse"
	pp "github.com/james-lawrence/torrent/peer_protocol"
	"github.com/james-lawrence/torrent/storage"
)

// Client contain zero or more Torrents. A Client manages a blocklist, the
// TCP/UDP protocol ports, and DHT as desired.
type Client struct {
	// An aggregate of stats over all connections. First in struct to ensure
	// 64-bit alignment of fields. See #262.
	stats ConnStats

	// locking counts, TODO remove these.
	lcount uint64
	ucount uint64

	_mu    *stdsync.RWMutex
	event  sync.Cond
	closed chan struct{}

	config *ClientConfig

	peerID          PeerID
	defaultStorage  *storage.Client
	onClose         []func()
	conns           []socket
	dhtServers      []*dht.Server
	ipBlockList     iplist.Ranger
	extensionBytes  pp.PeerExtensionBits // Our BitTorrent protocol extension bytes, sent in our BT handshakes.
	torrents        map[metainfo.Hash]*torrent
	dialRateLimiter *rate.Limiter
}

type ipStr string

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
	if dlt = cl.lookup(t); dlt != nil {
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

	dlt.updateWantPeersEvent()
	// Tickle Client.waitAccept, new torrent may want conns.
	cl.event.Broadcast()

	return dlt, true, nil
}

func (cl *Client) download(ctx context.Context, m Metadata, options ...Tuner) (t *torrent, err error) {
	if t, _, err = cl.start(m); err != nil {
		return t, err
	}

	t.Tune(options...)

	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return t, ctx.Err()
	}

	return t, nil
}

// Download the specified torrent.
// Download differs from Start in that it blocks until the metadata is downloaded and returns a
// valid reader for use.
func (cl *Client) Download(ctx context.Context, m Metadata, options ...Tuner) (r Reader, err error) {
	var (
		dlt *torrent
	)

	if dlt, err = cl.download(ctx, m, options...); err != nil {
		return r, err
	}

	return dlt.NewReader(), nil
}

// DownloadInto downloads the torrent into the provided destination. see Download for more information.
// TODO: context timeout only applies to fetching metadata, not the entire download.
func (cl *Client) DownloadInto(ctx context.Context, m Metadata, dst io.Writer, options ...Tuner) (err error) {
	var (
		dlt *torrent
	)

	if dlt, err = cl.download(ctx, m, options...); err != nil {
		return err
	}

	// copy into the destination
	// TODO: wrap the destination in a context writer to timeout the copy
	if n, err := io.Copy(dst, dlt.NewReader()); err != nil {
		return err
	} else if n != dlt.Length() {
		return errors.Errorf("download failed, missing data %d != %d", n, dlt.Length())
	}

	return nil
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

	if updated.ChunkSize != 0 {
		old.setChunkSize(pp.Integer(updated.ChunkSize))
	}

	old.addTrackers(updated.Trackers)
	old.lockedOpenNewConns()
	cl.event.Broadcast()

	return nil
}

func (cl *Client) lookup(t Metadata) *torrent {
	cl.lock()
	defer cl.unlock()

	if dl, ok := cl.torrents[t.InfoHash]; ok {
		return dl
	}

	return nil
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
	cl.eachDhtServer(func(s *dht.Server) {
		fmt.Fprintf(w, "%s DHT server at %s:\n", s.Addr().Network(), s.Addr().String())
		writeDhtServerStatus(w, s)
	})
	spew.Fdump(w, &cl.stats)
	fmt.Fprintf(w, "# Torrents: %d\n", len(cl.torrentsAsSlice()))
	fmt.Fprintln(w)
	for _, t := range slices.Sort(cl.torrentsAsSlice(), func(l, r Torrent) bool {
		return l.Metadata().InfoHash.AsString() < r.Metadata().InfoHash.AsString()
	}).([]Torrent) {
		metadata := t.Metadata()
		if metadata.DisplayName == "" {
			fmt.Fprint(w, "<unknown name>")
		} else {
			fmt.Fprint(w, metadata.DisplayName)
		}
		fmt.Fprint(w, "\n")

		if t, ok := t.(*torrent); ok {
			t.writeStatus(w)
		}
		fmt.Fprintln(w)
	}
}

func (cl *Client) announceKey() int32 {
	return int32(binary.BigEndian.Uint32(cl.peerID[16:20]))
}

// NewClient create a new client from the provided config. nil is acceptable.
func NewClient(cfg *ClientConfig) (_ *Client, err error) {
	if cfg == nil {
		cfg = NewDefaultClientConfig()
	}

	if cfg.HTTPProxy == nil && cfg.ProxyURL != "" {
		if fixedURL, err := url.Parse(cfg.ProxyURL); err == nil {
			cfg.HTTPProxy = http.ProxyURL(fixedURL)
		}
	}

	cl := &Client{
		config:          cfg,
		closed:          make(chan struct{}),
		torrents:        make(map[metainfo.Hash]*torrent),
		dialRateLimiter: rate.NewLimiter(10, 10),
		_mu:             &stdsync.RWMutex{},
	}

	defer func() {
		if err != nil {
			cl.Close()
		}
	}()

	cl.extensionBytes = defaultPeerExtensionBytes()
	cl.event.L = cl.locker()
	storageImpl := cfg.DefaultStorage
	if storageImpl == nil {
		// We'd use mmap but HFS+ doesn't support sparse files.
		storageImpl = storage.NewFile(cfg.DataDir)
		cl.onClose = append(cl.onClose, func() {
			if err := storageImpl.Close(); err != nil {
				cl.config.errors().Println(errors.Wrap(err, "error closing default storage"))
			}
		})
	}
	cl.defaultStorage = storage.NewClient(storageImpl)
	if cfg.IPBlocklist != nil {
		cl.ipBlockList = cfg.IPBlocklist
	}

	o := copy(cl.peerID[:], stringsx.Default(cfg.PeerID, cfg.Bep20))
	if _, err = rand.Read(cl.peerID[o:]); err != nil {
		return nil, errors.Wrap(err, "error generating peer id")
	}

	return cl, nil
}

func (cl *Client) firewallCallback(net.Addr) bool {
	block := !cl.wantConns()
	if block {
		metrics.Add("connections firewalled", 1)
	} else {
		metrics.Add("connections not firewalled", 1)
	}
	return block
}

func (cl *Client) newDhtServer(conn net.PacketConn) (s *dht.Server, err error) {
	cfg := dht.ServerConfig{
		IPBlocklist:    cl.ipBlockList,
		Conn:           conn,
		OnAnnouncePeer: cl.onDHTAnnouncePeer,
		PublicIP: func() net.IP {
			if connIsIpv6(conn) && cl.config.PublicIP6 != nil {
				return cl.config.PublicIP6
			}
			return cl.config.PublicIP4
		}(),
		StartingNodes:      cl.config.DhtStartingNodes,
		ConnectionTracking: cl.config.ConnTracker,
		OnQuery:            cl.config.DHTOnQuery,
	}

	if s, err = dht.NewServer(&cfg); err != nil {
		return s, err
	}
	go func() {
		ts, err := s.Bootstrap()
		if err != nil {
			cl.config.errors().Println(errors.Wrap(err, "error bootstrapping dht"))
		}
		cl.config.debug().Printf("%v completed bootstrap (%v)\n", s, ts)
	}()
	return s, nil
}

// Config underlying configuration for the client.
func (cl *Client) Config() *ClientConfig {
	return cl.config
}

// Bind the socket to this client.
func (cl *Client) Bind(s socket) (err error) {
	if err = cl.bindDHT(s); err != nil {
		return err
	}

	go cl.forwardPort()
	go cl.acceptConnections(s)

	cl.lock()
	cl.conns = append(cl.conns, s)
	cl.unlock()

	return nil
}

func (cl *Client) bindDHT(s socket) (err error) {
	var (
		ok bool
		pc net.PacketConn
	)

	if cl.config.NoDHT {
		return nil
	}

	if pc, ok = s.(net.PacketConn); !ok {
		return nil
	}

	ds, err := cl.newDhtServer(pc)
	if err != nil {
		return err
	}

	cl.dhtServers = append(cl.dhtServers, ds)

	return nil
}

// Closed returns a channel to detect when the client is closed.
func (cl *Client) Closed() <-chan struct{} {
	cl.rLock()
	defer cl.rUnlock()
	return cl.closed
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
	select {
	case <-cl.closed:
	default:
		close(cl.closed)
	}
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
		select {
		case <-cl.closed:
			return
		default:
		}

		if cl.wantConns() {
			return
		}

		cl.event.Wait()
	}
}

func (cl *Client) acceptConnections(l net.Listener) {
	var (
		err  error
		conn net.Conn
	)

	for {
		select {
		case <-cl.closed:
			if conn != nil {
				conn.Close()
			}
			return
		default:
		}

		if conn, err = cl.config.Handshaker.Accept(l); err != nil {
			cl.config.debug().Println(errors.Wrap(err, "error accepting connection"))
			continue
		}

		go cl.incomingConnection(conn)
	}
}

func (cl *Client) incomingConnection(nc net.Conn) {
	defer nc.Close()
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}

	c := cl.newConnection(nc, false, missinggo.IpPortFromNetAddr(nc.RemoteAddr()))
	c.Discovery = peerSourceIncoming
	cl.runReceivedConn(c)
}

// Torrent returns a handle to the given torrent, if it's present in the client.
func (cl *Client) Torrent(ih metainfo.Hash) (t Torrent, ok bool) {
	cl.lock()
	defer cl.unlock()
	t, ok = cl.torrents[ih]
	return
}

func (cl *Client) torrent(ih metainfo.Hash) *torrent {
	return cl.torrents[ih]
}

func reducedDialTimeout(minDialTimeout, max time.Duration, halfOpenLimit int, pendingPeers int) (ret time.Duration) {
	ret = max / time.Duration((pendingPeers+halfOpenLimit)/halfOpenLimit)
	if ret < minDialTimeout {
		ret = minDialTimeout
	}
	return
}

// Returns a connection over UTP or TCP, whichever is first to connect.
func (cl *Client) dialFirst(ctx context.Context, addr string) (conn net.Conn, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cl.lock()
	conns := make([]socket, len(cl.conns))
	left := copy(conns, cl.conns)
	_ = left
	cl.unlock()

	if len(conns) == 0 {
		return nil, errors.Errorf("unable to dial due to no servers")
	}

	for _, s := range conns {
		if conn, err = cl.dial(ctx, s, addr); err == nil {
			return conn, nil
		}
	}

	return nil, err

	// resCh := make(chan dialResult, left)
	// for _, cs := range conns {
	// 	go func(cs socket) {
	// 		conn, err := cl.dial(ctx, cs, addr)
	// 		log.Printf("dial result: %T - %s\n", conn, err)
	// 		resCh <- dialResult{
	// 			Conn:  conn,
	// 			cause: err,
	// 		}
	// 	}(cs)
	// }

	// // Wait for a successful connection.
	// for ; left > 0 && res.Conn == nil; left-- {
	// 	res = <-resCh
	// }

	// // There are still incompleted dials.
	// if left > 0 {
	// 	go func() {
	// 		for ; left > 0; left-- {
	// 			conn := (<-resCh).Conn
	// 			if conn != nil {
	// 				conn.Close()
	// 			}
	// 		}
	// 	}()
	// }

	// return res.Conn, res.cause
}

func (cl *Client) dial(ctx context.Context, d dialer, addr string) (c net.Conn, err error) {
	// Try to avoid committing to a dial if the context is complete as it's difficult to determine
	// which dial errors allow us to forget the connection tracking entry handle.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if c, err = d.Dial(ctx, addr); c == nil || err != nil {
		return nil, errorsx.Compact(err, ctx.Err(), errors.New("net.Conn missing (nil)"))
	}

	// This is a bit optimistic, but it looks non-trivial to thread this through the proxy code. Set
	// it now in case we close the connection forthwith.
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}

	return closeWrapper{
		Conn:   c,
		closer: c.Close,
	}, nil
}

func forgettableDialError(err error) bool {
	return strings.Contains(err.Error(), "no suitable address found")
}

// Performs initiator handshakes and returns a connection. Returns nil
// *connection if no connection for valid reasons.
func (cl *Client) handshakesConnection(ctx context.Context, nc net.Conn, t *torrent, encryptHeader bool, remoteAddr IpPort) (c *connection, err error) {
	dl := time.Now().Add(cl.config.HandshakesTimeout)
	ctx, cancel := context.WithDeadline(ctx, dl)
	defer cancel()

	if err = nc.SetDeadline(dl); err != nil {
		return nil, err
	}

	c = cl.newConnection(nc, true, remoteAddr)
	c.headerEncrypted = encryptHeader

	if err = cl.initiateHandshakes(c, t); err != nil {
		return nil, err
	}

	return c, nil
}

// Returns nil connection and nil error if no connection could be established
// for valid reasons.
func (cl *Client) establishOutgoingConnEx(t *torrent, addr IpPort, obfuscatedHeader bool) (c *connection, err error) {
	var (
		nc net.Conn
	)

	dialCtx, cancel := context.WithTimeout(context.Background(), t.dialTimeout())
	defer cancel()

	if nc, err = cl.dialFirst(dialCtx, addr.String()); err != nil {
		return nil, err
	}

	if c, err = cl.handshakesConnection(context.Background(), nc, t, obfuscatedHeader, addr); err != nil {
		nc.Close()
	}

	return c, err
}

// Returns nil connection and nil error if no connection could be established
// for valid reasons.
func (cl *Client) establishOutgoingConn(t *torrent, addr IpPort) (c *connection, err error) {
	obfuscatedHeaderFirst := cl.config.HeaderObfuscationPolicy.Preferred
	if c, err = cl.establishOutgoingConnEx(t, addr, obfuscatedHeaderFirst); err == nil {
		return c, nil
	}

	if cl.config.HeaderObfuscationPolicy.RequirePreferred {
		// We should have just tried with the preferred header obfuscation. If it was required,
		// there's nothing else to try.
		return c, err
	}

	// Try again with encryption if we didn't earlier, or without if we did.
	if c, err = cl.establishOutgoingConnEx(t, addr, !obfuscatedHeaderFirst); err != nil {
		return c, err
	}

	return c, err
}

// Called to dial out and run a connection. The addr we're given is already
// considered half-open.
func (cl *Client) outgoingConnection(t *torrent, addr IpPort, ps peerSource, trusted bool) {
	var (
		c   *connection
		err error
	)

	cl.dialRateLimiter.Wait(context.Background())

	if c, err = cl.establishOutgoingConn(t, addr); err != nil {
		t.noLongerHalfOpen(addr.String())
		cl.config.debug().Println(errors.Wrapf(err, "error establishing connection to %v", addr))
		return
	}
	t.noLongerHalfOpen(addr.String())

	c.Discovery = ps
	c.trusted = trusted

	// Since the remote address is almost never the same as the local bind address
	// due to network topologies (NAT, LAN, WAN) we have to detect this situation
	// from the origin of the connection and ban the address we connected to.
	if c.PeerID == cl.peerID {
		cl.config.Handshaker.Release(
			c.conn,
			connections.BannedConnectionError(
				c.conn,
				errors.Errorf("detected connection to self - banning %s", c.conn.RemoteAddr().String()),
			),
		)
		return
	}

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

func (cl *Client) initiateHandshakes(c *connection, t *torrent) (err error) {
	if c.headerEncrypted {
		var (
			rw io.ReadWriter
		)

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
		var firstIh metainfo.Hash
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
	var (
		rw       io.ReadWriter
		infoHash metainfo.Hash
	)
	rw, c.headerEncrypted, c.cryptoMethod, err = handleEncryption(c.rw(), cl.forSkeys, cl.config.HeaderObfuscationPolicy, cl.config.CryptoSelector)
	if err != nil {
		return nil, err
	}
	c.setRW(rw)

	if cl.config.HeaderObfuscationPolicy.RequirePreferred && c.headerEncrypted != cl.config.HeaderObfuscationPolicy.Preferred {
		return nil, errors.New("connection not have required header obfuscation")
	}

	if infoHash, err = cl.connBtHandshake(c, nil); err != nil {
		return nil, xerrors.Errorf("during bt handshake: %w", err)
	}

	cl.lock()
	t = cl.torrents[infoHash]
	cl.unlock()

	if t == nil {
		return nil, errors.New("received handshake for an unavailable torrent")
	}

	return t, nil
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
	if err := c.conn.SetDeadline(time.Now().Add(cl.config.HandshakesTimeout)); err != nil {
		cl.config.errors().Println(errors.Wrap(err, "failed setting handshake deadline"))
		return
	}

	t, err := cl.receiveHandshakes(c)
	if err != nil {
		cl.config.debug().Println(errors.Wrap(err, "error during handshake"))
		cl.config.Handshaker.Release(c.conn, connections.BannedConnectionError(c.conn, err))
		return
	}

	cl.runHandshookConn(c, t)
}

func (cl *Client) runHandshookConn(c *connection, t *torrent) {
	c.setTorrent(t)

	c.conn.SetWriteDeadline(time.Time{})
	c.r = deadlineReader{c.conn, c.r}
	completedHandshakeConnectionFlags.Add(c.connectionFlags(), 1)

	if err := t.addConnection(c); err != nil {
		cl.config.debug().Println(errors.Wrap(err, "error adding connection"))
		return
	}

	defer t.event.Broadcast()
	defer t.dropConnection(c)
	c.sendInitialMessages(cl, t)
	go c.writer(10 * time.Second)

	if err := c.mainReadLoop(); err != nil {
		cl.config.Handshaker.Release(c.conn, err)
		cl.config.debug().Println(errors.Wrap(err, "error during main read loop"))
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

// Return a Torrent ready for insertion into a Client.
func (cl *Client) newTorrent(src Metadata) (t *torrent) {
	if src.ChunkSize == 0 {
		src.ChunkSize = defaultChunkSize
	}

	t = newTorrent(cl, src)
	t.digests = newDigests(
		func(idx int) *Piece { return t.piece(idx) },
		func(idx int, cause error) {
			if err := t.pieceHashed(idx, cause); err != nil {
				cl.config.debug().Println(err)
			}
			t.updatePieceCompletion(idx)
			t.publishPieceChange(idx)
			t.updatePiecePriority(idx)

			t.event.Broadcast()
			cl.event.Broadcast()
		},
	)
	t.setInfoBytes(src.InfoBytes)
	t.addTrackers(src.Trackers)
	cl.AddDHTNodes(src.DHTNodes)

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

// WaitAll returns true when all torrents are completely downloaded and false if the
// client is stopped before that.
func (cl *Client) WaitAll() bool {
	cl.lock()
	defer cl.unlock()

	for !cl.allTorrentsCompleted() {
		select {
		case <-cl.closed:
			return false
		default:
		}

		cl.event.Wait()
	}

	return true
}

// Torrents returns handles to all the torrents loaded in the Client.
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

// DhtServers returns the set of DHT servers.
func (cl *Client) DhtServers() []*dht.Server {
	return cl.dhtServers
}

// AddDHTNodes adds nodes to the DHT servers.
func (cl *Client) AddDHTNodes(nodes []string) {
	for _, n := range nodes {
		hmp := missinggo.SplitHostMaybePort(n)
		ip := net.ParseIP(hmp.Host)
		if ip == nil {
			cl.config.info().Printf("refusing to add DHT node with invalid IP: %q\n", hmp.Host)
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

func (cl *Client) newConnection(nc net.Conn, outgoing bool, remoteAddr IpPort) (c *connection) {
	c = newConnection(nc, outgoing, remoteAddr)
	c.writerCond.L = c._mu
	c.setRW(connStatsReadWriter{nc, c})
	c.r = &rateLimitedReader{
		l: cl.config.DownloadRateLimiter,
		r: c.r,
	}
	cl.config.debug().Printf("initialized with remote %v (outgoing=%t)\n", remoteAddr, outgoing)
	return
}

func (cl *Client) onDHTAnnouncePeer(ih metainfo.Hash, ip net.IP, port int, portOk bool) {
	cl.config.DHTAnnouncePeer(ih, ip, port, portOk)
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

func (cl *Client) publicIP(peer net.IP) net.IP {
	// TODO: Use BEP 10 to determine how peers are seeing us.
	if peer.To4() != nil {
		return firstNotNil(
			cl.config.PublicIP4,
			cl.findListenerIP(func(ip net.IP) bool { return ip.To4() != nil }),
		)
	}

	return firstNotNil(
		cl.config.PublicIP6,
		cl.findListenerIP(func(ip net.IP) bool { return ip.To4() == nil }),
	)
}

func (cl *Client) findListenerIP(f func(net.IP) bool) net.IP {
	l := cl.findListener(func(l net.Listener) bool {
		return f(missinggo.AddrIP(l.Addr()))
	})

	if l == nil {
		return nil
	}

	return missinggo.AddrIP(l.Addr())
}

// Our IP as a peer should see it.
func (cl *Client) publicAddr(peer net.IP) IpPort {
	return IpPort{IP: cl.publicIP(peer), Port: uint16(cl.incomingPeerPort())}
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

var _ = atomic.AddInt32

func (cl *Client) rLock() {
	// updated := atomic.AddUint64(&cl.lcount, 1)
	// l2.Output(2, fmt.Sprintf("%p rlock initiated - %d", cl, updated))
	cl._mu.RLock()
	// l2.Output(2, fmt.Sprintf("%p rlock completed - %d", cl, updated))
}

func (cl *Client) rUnlock() {
	// updated := atomic.AddUint64(&cl.ucount, 1)
	// l2.Output(2, fmt.Sprintf("%p runlock initiated - %d", cl, updated))
	cl._mu.RUnlock()
	// l2.Output(2, fmt.Sprintf("%p runlock completed - %d", cl, updated))
}

func (cl *Client) lock() {
	// updated := atomic.AddUint64(&cl.lcount, 1)
	// l2.Output(2, fmt.Sprintf("%p lock initiated - %d", cl, updated))
	cl._mu.Lock()
	// l2.Output(2, fmt.Sprintf("%p lock completed - %d", cl, updated))
}

func (cl *Client) unlock() {
	// updated := atomic.AddUint64(&cl.ucount, 1)
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
	// updated := atomic.AddUint64(&cl.lcount, 1)
	// l2.Output(2, fmt.Sprintf("%p lock initiated - %d", cl.Client, updated))
	cl._mu.Lock()
	// l2.Output(2, fmt.Sprintf("%p lock completed - %d", cl.Client, updated))
}

func (cl clientLocker) Unlock() {
	// updated := atomic.AddUint64(&cl.ucount, 1)
	// l2.Output(2, fmt.Sprintf("%p unlock initiated - %d", cl.Client, updated))
	cl._mu.Unlock()
	// l2.Output(2, fmt.Sprintf("%p unlock completed - %d", cl.Client, updated))
}
