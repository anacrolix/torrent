package torrent

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	stdsync "sync"
	"sync/atomic"
	"time"

	"github.com/james-lawrence/torrent/connections"
	"github.com/james-lawrence/torrent/dht"
	"github.com/james-lawrence/torrent/dht/krpc"

	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/slices"
	"github.com/davecgh/go-spew/spew"
	"github.com/pkg/errors"

	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/stringsx"
	"github.com/james-lawrence/torrent/metainfo"
)

// Client contain zero or more Torrents. A Client manages a blocklist, the
// TCP/UDP protocol ports, and DHT as desired.
type Client struct {
	// An aggregate of stats over all connections. First in struct to ensure
	// 64-bit alignment of fields. See #262.
	stats ConnStats

	_mu    *stdsync.RWMutex
	event  stdsync.Cond
	closed chan struct{}

	config *ClientConfig

	peerID     PeerID
	onClose    []func()
	conns      []socket
	dhtServers []*dht.Server

	extensionBytes pp.ExtensionBits // Our BitTorrent protocol extension bytes, sent in our BT handshakes.
	torrents       map[metainfo.Hash]*torrent
}

// Query torrent info from the dht
func (cl *Client) Info(ctx context.Context, m Metadata, options ...Tuner) (i *metainfo.Info, err error) {
	var (
		t     *torrent
		added bool
	)

	if t, added, err = cl.start(m); err != nil {
		return nil, err
	} else if !added {
		log.Println("torrent already started?", m.ID, len(m.InfoBytes))
		return nil, fmt.Errorf("attempting to require the info for a torrent that is already running")
	}
	defer cl.Stop(m)

	t.Tune(options...)

	select {
	case <-t.GotInfo():
		return t.Info(), cl.Stop(m)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Start the specified torrent.
// Start adds starts up the torrent within the client downloading the missing pieces
// as needed. if you want to wait until the torrent is completed use Download.
func (cl *Client) Start(t Metadata, options ...Tuner) (dl Torrent, added bool, err error) {
	dl, added, err = cl.start(t)
	if err != nil {
		return dl, added, err
	}

	return dl, added, dl.Tune(options...)
}

// MaybeStart is a convience method that consumes the return types of the torrent
// creation methods: New, NewFromFile, etc. it is all respects identical to the Start
// method.
func (cl *Client) MaybeStart(t Metadata, failed error, options ...Tuner) (dl Torrent, added bool, err error) {
	if failed != nil {
		return dl, false, failed
	}

	dl, added, err = cl.start(t)
	if err != nil {
		return dl, added, err
	}

	return dl, added, dl.Tune(options...)
}

func (cl *Client) start(t Metadata) (dlt *torrent, added bool, err error) {
	if dlt = cl.lookup(t); dlt != nil {
		return dlt, false, cl.merge(dlt, t)
	}

	cl.lock()
	dlt, err = cl.newTorrent(t)
	cl.unlock()
	if err != nil {
		return nil, false, err
	}

	cl.merge(dlt, t)

	cl.lock()
	defer cl.unlock()

	cl.eachDhtServer(func(s *dht.Server) {
		go dlt.dhtAnnouncer(s)
	})
	cl.torrents[t.ID] = dlt

	dlt.updateWantPeersEvent()

	// Tickle Client.waitAccept, new torrent may want conns.
	cl.event.Broadcast()

	return dlt, true, nil
}

// Stop the specified torrent, this halts all network activity around the torrent
// for this client.
func (cl *Client) Stop(t Metadata) (err error) {
	return cl.dropTorrent(t.ID)
}

func (cl *Client) merge(old *torrent, updated Metadata) (err error) {
	if updated.DisplayName != "" {
		old.md.DisplayName = updated.DisplayName
	}

	if updated.ChunkSize != old.md.ChunkSize && updated.ChunkSize != 0 {
		old.setChunkSize(updated.ChunkSize)
	}

	old.openNewConns()
	cl.event.Broadcast()

	return nil
}

func (cl *Client) lookup(t Metadata) *torrent {
	cl.lock()
	defer cl.unlock()

	if dl, ok := cl.torrents[t.ID]; ok {
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
	fmt.Fprintf(w, "Announce key: %x\n", -1) // removed the method from the client didnt belong here.
	cl.eachDhtServer(func(s *dht.Server) {
		fmt.Fprintf(w, "%s DHT server at %s:\n", s.Addr().Network(), s.Addr().String())
		writeDhtServerStatus(w, s)
	})
	spew.Fdump(w, &cl.stats)
	fmt.Fprintf(w, "# Torrents: %d\n", len(cl.torrentsAsSlice()))
	fmt.Fprintln(w)
	for _, t := range slices.Sort(cl.torrentsAsSlice(), func(l, r Torrent) bool {
		return l.Metadata().ID.AsString() < r.Metadata().ID.AsString()
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

// NewClient create a new client from the provided config. nil is acceptable.
func NewClient(cfg *ClientConfig) (_ *Client, err error) {
	if cfg == nil {
		cfg = NewDefaultClientConfig(ClientConfigBootstrapGlobal)
	}

	if cfg.HTTPProxy == nil && cfg.ProxyURL != "" {
		if fixedURL, err := url.Parse(cfg.ProxyURL); err == nil {
			cfg.HTTPProxy = http.ProxyURL(fixedURL)
		}
	}

	cl := &Client{
		config:   cfg,
		closed:   make(chan struct{}),
		torrents: make(map[metainfo.Hash]*torrent),
		_mu:      &stdsync.RWMutex{},
	}

	defer func() {
		if err != nil {
			cl.Close()
		}
	}()

	cl.extensionBytes = defaultPeerExtensionBytes()
	cl.event.L = cl.locker()

	o := copy(cl.peerID[:], stringsx.Default(cfg.PeerID, cfg.Bep20))
	if _, err = rand.Read(cl.peerID[o:]); err != nil {
		return nil, errors.Wrap(err, "error generating peer id")
	}

	return cl, nil
}

func (cl *Client) newDhtServer(conn net.PacketConn) (s *dht.Server, err error) {
	cfg := dht.ServerConfig{
		OnAnnouncePeer: cl.onDHTAnnouncePeer,
		PublicIP: func() net.IP {
			if connIsIpv6(conn) && cl.config.PublicIP6 != nil {
				return cl.config.PublicIP6
			}
			return cl.config.PublicIP4
		}(),
		StartingNodes: cl.config.DhtStartingNodes(conn.LocalAddr().Network()),
		OnQuery:       cl.config.DHTOnQuery,
		Logger:        newlogger(cl.config.Logger, "dht", log.Flags()),
		BucketLimit:   cl.config.bucketLimit,
	}

	if s, err = dht.NewServer(&cfg); err != nil {
		return s, err
	}

	go func() {
		if err = s.ServeMux(context.Background(), conn, cl.config.DHTMuxer); err != nil {
			log.Println("dht failed", err)
		}
	}()

	go func() {
		ts, err := s.Bootstrap(context.Background())
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
	copy(conns, cl.conns)
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

	// type dialResult struct {
	// 	Conn net.Conn
	// 	cause error
	// }
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

// Returns nil connection and nil error if no connection could be established
// for valid reasons.
func (cl *Client) establishOutgoingConnEx(ctx context.Context, t *torrent, addr IpPort, obfuscatedHeader bool) (c *connection, err error) {
	var (
		nc net.Conn
	)

	dialCtx, dcancel := context.WithTimeout(ctx, t.dialTimeout())
	defer dcancel()

	if nc, err = cl.dialFirst(dialCtx, addr.String()); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			nc.Close()
		}
	}()

	dl := time.Now().Add(cl.config.HandshakesTimeout)
	if err = nc.SetDeadline(dl); err != nil {
		return nil, err
	}

	c = cl.newConnection(nc, true, addr)
	c.headerEncrypted = obfuscatedHeader

	if err = cl.initiateHandshakes(c, t); err != nil {
		return nil, err
	}

	return c, err
}

// Returns nil connection and nil error if no connection could be established
// for valid reasons.
func (cl *Client) establishOutgoingConn(ctx context.Context, t *torrent, addr IpPort) (c *connection, err error) {
	obfuscatedHeaderFirst := cl.config.HeaderObfuscationPolicy.Preferred
	if c, err = cl.establishOutgoingConnEx(ctx, t, addr, obfuscatedHeaderFirst); err == nil {
		return c, nil
	}

	if cl.config.HeaderObfuscationPolicy.RequirePreferred {
		// We should have just tried with the preferred header obfuscation. If it was required,
		// there's nothing else to try.
		return c, err
	}

	// Try again with encryption if we didn't earlier, or without if we did.
	if c, err = cl.establishOutgoingConnEx(ctx, t, addr, !obfuscatedHeaderFirst); err != nil {
		return c, err
	}

	return c, err
}

// Called to dial out and run a connection. The addr we're given is already
// considered half-open.
func (cl *Client) outgoingConnection(ctx context.Context, t *torrent, addr IpPort, ps peerSource, trusted bool) {
	var (
		c   *connection
		err error
	)

	cl.config.info().Println("opening connection", t.md.ID)
	if err = cl.config.dialRateLimiter.Wait(ctx); err != nil {
		log.Println("dial rate limit failed", err)
		return
	}

	if c, err = cl.establishOutgoingConn(ctx, t, addr); err != nil {
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

func (cl *Client) initiateHandshakes(c *connection, t *torrent) (err error) {
	var (
		rw io.ReadWriter
	)
	rw = c.rw()

	if c.headerEncrypted {
		rw, c.cryptoMethod, err = pp.EncryptionHandshake{
			Keys:           cl.forSkeys,
			CryptoSelector: cl.config.CryptoSelector,
		}.Outgoing(rw, t.md.ID[:], cl.config.CryptoProvides)

		if err != nil {
			return errors.Wrap(err, "encryption handshake failed")
		}
	}
	c.setRW(rw)

	ebits, info, err := pp.Handshake{
		PeerID: cl.peerID,
		Bits:   cl.extensionBytes,
	}.Outgoing(c.rw(), t.md.ID)

	if err != nil {
		return errors.Wrap(err, "bittorrent protocol handshake failure")
	}

	c.PeerExtensionBytes = ebits
	c.PeerID = info.PeerID
	c.completedHandshake = time.Now()

	return nil
}

// Do encryption and bittorrent handshakes as receiver.
func (cl *Client) receiveHandshakes(c *connection) (t *torrent, err error) {
	var (
		buffered io.ReadWriter
	)

	encryption := pp.EncryptionHandshake{
		Keys:           cl.forSkeys,
		CryptoSelector: cl.config.CryptoSelector,
	}

	if _, buffered, err = encryption.Incoming(c.rw()); err != nil && cl.config.HeaderObfuscationPolicy.RequirePreferred {
		return t, errors.Wrap(err, "connection does not have the required header obfuscation")
	} else if err != nil && buffered == nil {
		cl.config.debug().Println("encryption handshake failed", err)
		return nil, err
	} else if err != nil {
		cl.config.debug().Println("encryption handshake failed", err)
	}

	ebits, info, err := pp.Handshake{
		PeerID: cl.peerID,
		Bits:   cl.extensionBytes,
	}.Incoming(buffered)

	if err != nil {
		return nil, errors.Wrap(err, "invalid handshake failed")
	}

	c.PeerExtensionBytes = ebits
	c.PeerID = info.PeerID
	c.completedHandshake = time.Now()

	cl.lock()
	t = cl.torrents[info.Hash]
	cl.unlock()

	if t == nil {
		return nil, errors.New("received handshake for an unavailable torrent")
	}

	return t, nil
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
func (cl *Client) newTorrent(src Metadata) (t *torrent, _ error) {
	if src.ChunkSize == 0 {
		src.ChunkSize = defaultChunkSize
	}

	t = newTorrent(cl, src)

	if len(src.InfoBytes) > 0 {
		if err := t.setInfoBytes(src.InfoBytes); err != nil {
			log.Println("encountered an error setting info bytes", len(src.InfoBytes))
			return nil, err
		}
	}

	cl.AddDHTNodes(src.DHTNodes)

	return t, nil
}

// Handle a file-like handle to some torrent data resource.
type Handle interface {
	io.Reader
	io.Seeker
	io.Closer
	io.ReaderAt
}

func (cl *Client) dropTorrent(infoHash metainfo.Hash) (err error) {
	cl.rLock()
	t, ok := cl.torrents[infoHash]
	cl.rUnlock()

	if !ok {
		// if there isnt a torrent there isnt a problem.
		return nil
	}

	cl.lock()
	delete(cl.torrents, infoHash)
	cl.unlock()

	return t.close()
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
			Addr: krpc.NewNodeAddrFromIPPort(ip, hmp.Port),
		}
		cl.eachDhtServer(func(s *dht.Server) {
			s.AddNode(ni)
		})
	}
}

func (cl *Client) newConnection(nc net.Conn, outgoing bool, remoteAddr IpPort) (c *connection) {
	c = newConnection(nc, outgoing, remoteAddr)
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

func (cl *Client) locker() stdsync.Locker {
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
