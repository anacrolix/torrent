package dht

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/pprof"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/v2/conntrack"
	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
	"github.com/james-lawrence/torrent/iplist"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/x/langx"
	"github.com/pkg/errors"

	"github.com/anacrolix/stm"
)

// A Server defines parameters for a DHT node server that is able to send
// queries, and respond to the ones from the network. Each node has a globally
// unique identifier known as the "node ID." Node IDs are chosen at random
// from the same 160-bit space as BitTorrent infohashes and define the
// behaviour of the node. Zero valued Server does not have a valid ID and thus
// is unable to function properly. Use `NewServer(nil)` to initialize a
// default node.
type Server struct {
	id          int160
	socket      net.PacketConn
	resendDelay func() time.Duration

	mu           sync.RWMutex
	transactions map[transactionKey]*Transaction
	mux          Muxer
	nextT        uint64 // unique "t" field for outbound queries
	table        table
	closed       missinggo.Event
	ipBlockList  iplist.Ranger
	tokenServer  tokenServer // Manages tokens we issue to our queriers.
	config       ServerConfig
	stats        ServerStats
	sendLimit    interface {
		Wait(ctx context.Context) error
		Allow() bool
		AllowStm(tx *stm.Tx) bool
	}
}

func (s *Server) numGoodNodes() (num int) {
	s.table.forNodes(func(n *node) bool {
		if n.IsGood() {
			num++
		}
		return true
	})
	return
}

func prettySince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	d /= time.Second
	d *= time.Second
	return fmt.Sprintf("%s ago", d)
}

func (s *Server) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "Listening on %s\n", s.Addr())
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(w, "Nodes in table: %d good, %d total\n", s.numGoodNodes(), s.numNodes())
	fmt.Fprintf(w, "Ongoing transactions: %d\n", len(s.transactions))
	fmt.Fprintf(w, "Server node ID: %x\n", s.id.Bytes())
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
	fmt.Fprintf(tw, "b#\tnode id\taddr\tanntok\tlast query\tlast response\tcf\n")
	for i, b := range s.table.buckets {
		b.EachNode(func(n *node) bool {
			fmt.Fprintf(tw, "%d\t%x\t%s\t%v\t%s\t%s\t%d\n",
				i,
				n.id.Bytes(),
				n.addr,
				func() int {
					if n.announceToken == nil {
						return -1
					}
					return len(*n.announceToken)
				}(),
				prettySince(n.lastGotQuery),
				prettySince(n.lastGotResponse),
				n.consecutiveFailures,
			)
			return true
		})
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func (s *Server) numNodes() (num int) {
	s.table.forNodes(func(n *node) bool {
		num++
		return true
	})
	return
}

// Stats returns statistics for the server.
func (s *Server) Stats() ServerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.stats
	ss.GoodNodes = s.numGoodNodes()
	ss.Nodes = s.numNodes()
	ss.OutstandingTransactions = len(s.transactions)
	return ss
}

// Addr returns the listen address for the server. Packets arriving to this address
// are processed by the server (unless aliens are involved).
func (s *Server) Addr() net.Addr {
	return s.socket.LocalAddr()
}

// NewDefaultServerConfig ...
func NewDefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		NoSecurity:         true,
		StartingNodes:      GlobalBootstrapAddrs,
		ConnectionTracking: conntrack.NewInstance(),
	}
}

// NewServer initializes a new DHT node server.
func NewServer(c *ServerConfig) (s *Server, err error) {
	if c == nil {
		c = NewDefaultServerConfig()
	}
	if missinggo.IsZeroValue(c.NodeId) {
		c.NodeId = RandomNodeID()
		if !c.NoSecurity && c.PublicIP != nil {
			SecureNodeId(&c.NodeId, c.PublicIP)
		}
	}

	// If Logger is empty, emulate the old behaviour: Everything is logged to stderr location,
	// and there are no debug messages.
	if c.Logger == nil {
		c.Logger = log.Default()
	}

	s = &Server{
		config:      *c,
		ipBlockList: c.IPBlocklist,
		mux:         DefaultMuxer(),
		tokenServer: tokenServer{
			maxIntervalDelta: 2,
			interval:         5 * time.Minute,
			secret:           make([]byte, 20),
		},
		transactions: make(map[transactionKey]*Transaction),
		table: table{
			k: 8,
		},
		sendLimit: defaultSendLimiter,
	}
	if s.config.ConnectionTracking == nil {
		s.config.ConnectionTracking = conntrack.NewInstance()
	}
	rand.Read(s.tokenServer.secret)
	s.id = int160FromByteArray(c.NodeId)
	s.table.rootID = s.id
	s.resendDelay = s.config.QueryResendDelay
	if s.resendDelay == nil {
		s.resendDelay = defaultQueryResendDelay
	}

	return
}

func NewMuxer() Muxer {
	return defaultMuxer{
		m:        make(map[string]Handler, 10),
		fallback: UnimplementedHandler{},
	}
}

// Standard Muxer configuration used by the server.
func DefaultMuxer() Muxer {
	m := NewMuxer()
	m.Method("ping", HandlerPing{})
	m.Method("get_peers", HandlerPeers{})
	m.Method("find_node", HandlerNearestPeer{})
	m.Method("announce_peer", HandlerAnnounce{})
	return m
}

type defaultMuxer struct {
	m        map[string]Handler
	fallback Handler
}

func (t defaultMuxer) Method(name string, fn Handler) {
	t.m[name] = fn
}

func (t defaultMuxer) Handler(r *krpc.Msg) (pattern string, fn Handler) {
	if fn, ok := t.m[r.Q]; ok {
		return r.Q, fn
	}

	return r.Q, t.fallback
}

type Handler interface {
	Handle(ctx context.Context, src Addr, srv *Server, msg *krpc.Msg) error
}

type UnimplementedHandler struct{}

func (t UnimplementedHandler) Handle(ctx context.Context, source Addr, s *Server, m *krpc.Msg) error {
	log.Println("unimplemented rpc method was received", m.Q, source.String())
	return s.sendError(ctx, source, m.T, krpc.ErrorMethodUnknown)
}

type HandlerPing struct{}

func (t HandlerPing) Handle(ctx context.Context, src Addr, srv *Server, msg *krpc.Msg) error {
	return srv.reply(ctx, src, msg.T, krpc.Return{})
}

type HandlerPeers struct{}

func (t HandlerPeers) Handle(ctx context.Context, source Addr, s *Server, m *krpc.Msg) error {
	var r krpc.Return

	if err := s.setReturnNodes(&r, *m, source); err != nil {
		s.sendError(ctx, source, m.T, *err)
		return nil
	}

	r.Token = langx.Autoptr(s.createToken(source))
	return s.reply(ctx, source, m.T, r)
}

type HandlerAnnounce struct{}

func (t HandlerAnnounce) Handle(ctx context.Context, source Addr, s *Server, m *krpc.Msg) error {
	readAnnouncePeer.Add(1)
	if !s.validToken(m.A.Token, source) {
		log.Println("invalid announce token received from:", source.String())
		return nil
	}

	if h := s.config.OnAnnouncePeer; h != nil {
		var port int
		portOk := false
		if m.A.Port != nil {
			port = *m.A.Port
			portOk = true
		}
		if m.A.ImpliedPort {
			port = source.Port()
			portOk = true
		}
		go h(metainfo.Hash(m.A.InfoHash), source.IP(), port, portOk)
	}
	return s.reply(ctx, source, m.T, krpc.Return{})
}

// locates the nearest peer.
type HandlerNearestPeer struct{}

func (t HandlerNearestPeer) Handle(ctx context.Context, source Addr, s *Server, m *krpc.Msg) error {
	var r krpc.Return
	if err := s.setReturnNodes(&r, *m, source); err != nil {
		return s.sendError(ctx, source, m.T, *err)
	}
	return s.reply(ctx, source, m.T, r)
}

type Muxer interface {
	Method(name string, fn Handler)
	Handler(r *krpc.Msg) (pattern string, fn Handler)
}

func (s *Server) ServeMux(ctx context.Context, c net.PacketConn, m Muxer) error {
	s.mu.Lock()
	s.mux = m
	s.socket = c
	s.mu.Unlock()

	return s.serveUntilClosed()
}

func (s *Server) handleQuery(ctx context.Context, source Addr, m krpc.Msg) {
	var (
		pattern string
		fn      Handler
	)

	if m.SenderID() != nil {
		if n, _ := s.getNode(source, int160FromByteArray(*m.SenderID()), !m.ReadOnly); n != nil {
			n.lastGotQuery = time.Now()
		}
	}

	if s.config.OnQuery != nil {
		propagate := s.config.OnQuery(&m, source.Raw())
		if !propagate {
			return
		}
	}

	if pattern, fn = s.mux.Handler(&m); fn == nil {
		log.Println("unable to locate a handler for", pattern)
		return
	}

	if err := fn.Handle(ctx, source, s, &m); err != nil {
		log.Println("query failed", source.String(), err)
		return
	}
}

func (s *Server) serveUntilClosed() error {
	err := s.serve()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.IsSet() {
		return err // this is a change in behavior may expose other issues. use to be nil.
	}

	return err
}

// Returns a description of the Server.
func (s *Server) String() string {
	return fmt.Sprintf("dht server on %s", s.socket.LocalAddr())
}

// SetIPBlockList packets to and from any address matching a range in the list are dropped.
func (s *Server) SetIPBlockList(list iplist.Ranger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ipBlockList = list
}

// IPBlocklist ....
func (s *Server) IPBlocklist() iplist.Ranger {
	return s.ipBlockList
}

func (s *Server) processPacket(ctx context.Context, b []byte, addr Addr) {
	if len(b) < 2 || b[0] != 'd' {
		// KRPC messages are bencoded dicts.
		readNotKRPCDict.Add(1)
		return
	}
	var d krpc.Msg
	err := bencode.Unmarshal(b, &d)
	if _err, ok := err.(bencode.ErrUnusedTrailingBytes); ok {
		log.Printf("%s: received message packet with %d trailing bytes: %q", s, _err.NumUnusedBytes, b[len(b)-_err.NumUnusedBytes:])
	} else if err != nil {
		readUnmarshalError.Add(1)
		if se, ok := err.(*bencode.SyntaxError); ok {
			// The message was truncated.
			if int(se.Offset) == len(b) {
				return
			}
			// Some messages seem to drop to nul chars abrubtly.
			if int(se.Offset) < len(b) && b[se.Offset] == 0 {
				return
			}
			// The message isn't bencode from the first.
			if se.Offset == 0 {
				return
			}
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.IsSet() {
		return
	}
	var n *node
	if sid := d.SenderID(); sid != nil {
		n, _ = s.getNode(addr, int160FromByteArray(*sid), !d.ReadOnly)
		if n != nil && d.ReadOnly {
			n.readOnly = true
		}
	}
	if d.Y == "q" {
		s.logger().Printf("received query %q from %v", d.Q, addr)
		s.handleQuery(ctx, addr, d)
		return
	}
	tk := transactionKey{
		RemoteAddr: addr.String(),
		T:          d.T,
	}
	t, ok := s.transactions[tk]
	if !ok {
		s.logger().Printf("received response for untracked transaction %q from %v", d.T, addr)
		return
	}
	s.logger().Printf("received response for transaction %q from %v", d.T, addr)
	go t.handleResponse(d)
	if n != nil {
		n.lastGotResponse = time.Now()
		n.consecutiveFailures = 0
	}
	// Ensure we don't send more than one response.
	s.deleteTransaction(tk)
}

func (s *Server) serve() error {
	var b [65536]byte
	for {
		n, addr, err := s.socket.ReadFrom(b[:])
		if err != nil {
			return err
		}

		if n == len(b) {
			return errors.New("received dht packet exceeds buffer size")
		}

		if missinggo.AddrPort(addr) == 0 {
			readZeroPort.Add(1)
			continue
		}
		s.mu.Lock()
		blocked := s.ipBlocked(missinggo.AddrIP(addr))
		s.mu.Unlock()
		if blocked {
			readBlocked.Add(1)
			continue
		}
		s.processPacket(context.Background(), b[:n], NewAddr(addr))
	}
}

func (s *Server) ipBlocked(ip net.IP) (blocked bool) {
	if s.ipBlockList == nil {
		return
	}
	_, blocked = s.ipBlockList.Lookup(ip)
	return
}

// Adds directly to the node table.
func (s *Server) AddNode(ni krpc.NodeInfo) error {
	id := int160FromByteArray(ni.ID)
	if id.IsZero() {
		return s.Ping(ni.Addr.UDP(), nil)
	}
	_, err := s.getNode(NewAddr(ni.Addr.UDP()), int160FromByteArray(ni.ID), true)
	return err
}

func wantsContain(ws []krpc.Want, w krpc.Want) bool {
	for _, _w := range ws {
		if _w == w {
			return true
		}
	}
	return false
}

func shouldReturnNodes(queryWants []krpc.Want, querySource net.IP) bool {
	if len(queryWants) != 0 {
		return wantsContain(queryWants, krpc.WantNodes)
	}
	return querySource.To4() != nil
}

func shouldReturnNodes6(queryWants []krpc.Want, querySource net.IP) bool {
	if len(queryWants) != 0 {
		return wantsContain(queryWants, krpc.WantNodes6)
	}
	return querySource.To4() == nil
}

func (s *Server) makeReturnNodes(target int160, filter func(krpc.NodeAddr) bool) []krpc.NodeInfo {
	return s.closestGoodNodeInfos(8, target, filter)
}

var krpcErrMissingArguments = krpc.Error{
	Code: krpc.ErrorCodeProtocolError,
	Msg:  "missing arguments dict",
}

func (s *Server) setReturnNodes(r *krpc.Return, queryMsg krpc.Msg, querySource Addr) *krpc.Error {
	if queryMsg.A == nil {
		return &krpcErrMissingArguments
	}
	target := int160FromByteArray(queryMsg.A.InfoHash)
	if shouldReturnNodes(queryMsg.A.Want, querySource.IP()) {
		r.Nodes = s.makeReturnNodes(target, func(na krpc.NodeAddr) bool { return na.IP.To4() != nil })
	}
	if shouldReturnNodes6(queryMsg.A.Want, querySource.IP()) {
		r.Nodes6 = s.makeReturnNodes(target, func(krpc.NodeAddr) bool { return true })
	}
	return nil
}

func (s *Server) sendError(ctx context.Context, addr Addr, t string, e krpc.Error) error {
	m := krpc.Msg{
		T: t,
		Y: "e",
		E: &e,
	}
	b, err := bencode.Marshal(m)
	if err != nil {
		return err
	}
	s.logger().Printf("sending error to %q: %v", addr, e)
	_, err = s.writeToNode(ctx, b, addr, false, true)
	if err != nil {
		return err
	}

	return nil
}

func (s *Server) reply(ctx context.Context, addr Addr, t string, r krpc.Return) error {
	r.ID = s.id.AsByteArray()
	m := krpc.Msg{
		T:  t,
		Y:  "r",
		R:  &r,
		IP: addr.KRPC(),
	}
	b, err := bencode.Marshal(m)
	if err != nil {
		return err
	}

	wrote, err := s.writeToNode(ctx, b, addr, false, true)
	if err != nil {
		return err
	}
	if wrote {
		expvars.Add("replied to peer", 1)
	}

	return nil
}

// Returns the node if it's in the routing table, adding it if appropriate.
func (s *Server) getNode(addr Addr, id int160, tryAdd bool) (*node, error) {
	if n := s.table.getNode(addr, id); n != nil {
		return n, nil
	}
	n := &node{nodeKey: nodeKey{
		id:   id,
		addr: addr,
	}}
	// Check that the node would be good to begin with. (It might have a bad
	// ID or banned address, or we fucked up the initial node field
	// invariant.)
	if err := s.nodeErr(n); err != nil {
		return nil, err
	}
	if !tryAdd {
		return nil, errors.New("node not present and add flag false")
	}
	b := s.table.bucketForID(id)
	if b.Len() >= s.table.k {
		if b.EachNode(func(n *node) bool {
			if s.nodeIsBad(n) {
				s.table.dropNode(n)
			}
			return b.Len() >= s.table.k
		}) {
			return nil, errors.New("no room in bucket")
		}
	}
	if err := s.table.addNode(n); err != nil {
		panic(fmt.Sprintf("expected to add node: %s", err))
	}
	return n, nil
}

func (s *Server) nodeIsBad(n *node) bool {
	return s.nodeErr(n) != nil
}

func (s *Server) nodeErr(n *node) error {
	if n.id == s.id {
		return errors.New("is self")
	}
	if n.id.IsZero() {
		return errors.New("has zero id")
	}
	if !s.config.NoSecurity && !n.IsSecure() {
		return errors.New("not secure")
	}
	if n.IsGood() {
		return nil
	}
	if n.consecutiveFailures >= 3 {
		return fmt.Errorf("has %d consecutive failures", n.consecutiveFailures)
	}
	return nil
}

func (s *Server) writeToNode(ctx context.Context, b []byte, node Addr, wait, rate bool) (wrote bool, err error) {
	if list := s.ipBlockList; list != nil {
		if r, ok := list.Lookup(node.IP()); ok {
			err = fmt.Errorf("write to %v blocked by %v", node, r)
			return
		}
	}
	//s.config.Logger.WithValues(log.Debug).Printf("writing to %s: %q", node.String(), b)
	if rate {
		if wait {
			err = s.sendLimit.Wait(ctx)
			if err != nil {
				return false, err
			}
		} else {
			if !s.sendLimit.Allow() {
				return false, errors.New("rate limit exceeded")
			}
		}
	}
	n, err := s.socket.WriteTo(b, node.Raw())
	writes.Add(1)
	if rate {
		expvars.Add("rated writes", 1)
	} else {
		expvars.Add("unrated writes", 1)
	}
	if err != nil {
		writeErrors.Add(1)
		err = fmt.Errorf("error writing %d bytes to %s: %s", len(b), node, err)
		return
	}
	wrote = true
	if n != len(b) {
		err = io.ErrShortWrite
		return
	}
	return
}

func (s *Server) nextTransactionID() string {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], s.nextT)
	s.nextT++
	return string(b[:n])
}

func (s *Server) deleteTransaction(k transactionKey) {
	delete(s.transactions, k)
}

func (s *Server) addTransaction(k transactionKey, t *Transaction) {
	if _, ok := s.transactions[k]; ok {
		panic("transaction not unique")
	}
	s.transactions[k] = t
}

// ID returns the 20-byte server ID. This is the ID used to communicate with the
// DHT network.
func (s *Server) ID() [20]byte {
	return s.id.AsByteArray()
}

func (s *Server) createToken(addr Addr) string {
	return s.tokenServer.CreateToken(addr)
}

func (s *Server) validToken(token string, addr Addr) bool {
	return s.tokenServer.ValidToken(token, addr)
}

func (s *Server) connTrackEntryForAddr(a Addr) conntrack.Entry {
	return conntrack.Entry{
		Protocol:   s.socket.LocalAddr().Network(),
		LocalAddr:  s.socket.LocalAddr().String(),
		RemoteAddr: a.String(),
	}
}

type numWrites int

func (s *Server) beginQuery(addr Addr, reason string, f func() numWrites) stm.Operation {
	return func(tx *stm.Tx) interface{} {
		tx.Assert(s.sendLimit.AllowStm(tx))
		cteh := s.config.ConnectionTracking.Allow(tx, s.connTrackEntryForAddr(addr), reason, -1)
		tx.Assert(cteh != nil)
		return func() {
			writes := f()
			finalizeCteh(cteh, writes)
		}
	}
}

func (s *Server) query(addr Addr, q string, a *krpc.MsgArgs, callback func(krpc.Msg, error)) error {
	if callback == nil {
		callback = func(krpc.Msg, error) {}
	}
	go func() {
		stm.Atomically(
			s.beginQuery(addr, fmt.Sprintf("send dht query %q", q),
				func() numWrites {
					m, writes, err := s.queryContext(context.Background(), addr, q, a)
					callback(m, err)
					return writes
				},
			),
		).(func())()
	}()
	return nil
}

func (s *Server) makeQueryBytes(q string, a *krpc.MsgArgs, t string) []byte {
	if a == nil {
		a = &krpc.MsgArgs{}
	}
	a.ID = s.ID()
	m := krpc.Msg{
		T: t,
		Y: "q",
		Q: q,
		A: a,
	}

	b, err := bencode.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func (s *Server) queryContext(ctx context.Context, addr Addr, q string, a *krpc.MsgArgs) (reply krpc.Msg, writes numWrites, err error) {
	defer func(started time.Time) {
		s.logger().Printf(
			"queryContext(%v) returned after %v (err=%v, reply.Y=%v, reply.E=%v, writes=%v)",
			q, time.Since(started), err, reply.Y, reply.E, writes)
	}(time.Now())
	replyChan := make(chan krpc.Msg, 1)
	t := &Transaction{
		onResponse: func(m krpc.Msg) {
			replyChan <- m
		},
	}
	tk := transactionKey{
		RemoteAddr: addr.String(),
	}
	s.mu.Lock()
	tid := s.nextTransactionID()
	s.stats.OutboundQueriesAttempted++
	tk.T = tid
	s.addTransaction(tk, t)
	s.mu.Unlock()
	sendErr := make(chan error, 1)
	sendCtx, cancelSend := context.WithCancel(ctx)
	defer cancelSend()
	go pprof.Do(sendCtx, pprof.Labels("q", q), func(ctx context.Context) {
		s.transactionQuerySender(ctx, sendErr, s.makeQueryBytes(q, a, tid), &writes, addr)
	})
	expvars.Add(fmt.Sprintf("outbound %s queries", q), 1)
	select {
	case reply = <-replyChan:
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-sendErr:
	}
	s.mu.Lock()
	s.deleteTransaction(tk)
	if err != nil {
		for _, n := range s.table.addrNodes(addr) {
			n.consecutiveFailures++
		}
	}
	s.mu.Unlock()
	return
}

func (s *Server) transactionQuerySender(sendCtx context.Context, sendErr chan<- error, b []byte, writes *numWrites, addr Addr) {
	defer close(sendErr)
	err := transactionSender(
		sendCtx,
		func() error {
			wrote, err := s.writeToNode(sendCtx, b, addr, *writes == 0, *writes != 0)
			if wrote {
				*writes++
			}
			return err
		},
		s.resendDelay,
		maxTransactionSends,
	)
	if err != nil {
		sendErr <- err
		return
	}
	select {
	case <-sendCtx.Done():
		sendErr <- sendCtx.Err()
	case <-time.After(s.resendDelay()):
		sendErr <- errors.New("timed out")
	}

}

// Sends a ping query to the address given.
func (s *Server) Ping(node *net.UDPAddr, callback func(krpc.Msg, error)) error {
	return s.ping(node, callback)
}

func (s *Server) ping(node *net.UDPAddr, callback func(krpc.Msg, error)) error {
	return s.query(NewAddr(node), "ping", nil, callback)
}

func (s *Server) announcePeer(node Addr, infoHash int160, port int, token string, impliedPort bool) (m krpc.Msg, writes numWrites, err error) {
	if port == 0 && !impliedPort {
		err = errors.New("no port specified")
		return
	}
	m, writes, err = s.queryContext(
		context.TODO(), node, "announce_peer",
		&krpc.MsgArgs{
			ImpliedPort: impliedPort,
			InfoHash:    infoHash.AsByteArray(),
			Port:        &port,
			Token:       token,
		},
	)
	if err != nil {
		return
	}
	if err = m.Error(); err != nil {
		announceErrors.Add(1)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.SuccessfulOutboundAnnouncePeerQueries++
	return
}

// Add response nodes to node table.
func (s *Server) addResponseNodes(d krpc.Msg) {
	if d.R == nil {
		return
	}
	d.R.ForAllNodes(func(ni krpc.NodeInfo) {
		s.getNode(NewAddr(ni.Addr.UDP()), int160FromByteArray(ni.ID), true)
	})
}

// Sends a find_node query to addr. targetID is the node we're looking for.
func (s *Server) findNode(addr Addr, targetID int160) (krpc.Msg, numWrites, error) {
	m, writes, err := s.queryContext(context.TODO(), addr, "find_node", &krpc.MsgArgs{
		Target: targetID.AsByteArray(),
		Want:   []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
	})
	// Scrape peers from the response to put in the server's table before
	// handing the response back to the caller.
	s.mu.Lock()
	s.addResponseNodes(m)
	s.mu.Unlock()
	return m, writes, err
}

// Returns how many nodes are in the node table.
func (s *Server) NumNodes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.numNodes()
}

// Exports the current node table.
func (s *Server) Nodes() (nis []krpc.NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.table.forNodes(func(n *node) bool {
		nis = append(nis, krpc.NodeInfo{
			Addr: n.addr.KRPC(),
			ID:   n.id.AsByteArray(),
		})
		return true
	})
	return
}

// Stops the server network activity. This is all that's required to clean-up a Server.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed.Set()
	if s.socket != nil {
		s.socket.Close()
	}
}

func (s *Server) getPeers(ctx context.Context, addr Addr, infoHash int160) (krpc.Msg, numWrites, error) {
	m, writes, err := s.queryContext(ctx, addr, "get_peers", &krpc.MsgArgs{
		InfoHash: infoHash.AsByteArray(),
		// TODO: Maybe IPv4-only Servers won't want IPv6 nodes?
		Want: []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addResponseNodes(m)
	if m.R != nil {
		if m.R.Token == nil {
			expvars.Add("get_peers responses with no token", 1)
		} else if len(*m.R.Token) == 0 {
			expvars.Add("get_peers responses with empty token", 1)
		} else {
			expvars.Add("get_peers responses with token", 1)
		}
		if m.SenderID() != nil && m.R.Token != nil {
			if n, _ := s.getNode(addr, int160FromByteArray(*m.SenderID()), false); n != nil {
				n.announceToken = m.R.Token
			}
		}
	}
	return m, writes, err
}

func (s *Server) closestGoodNodeInfos(
	k int,
	targetID int160,
	filter func(krpc.NodeAddr) bool,
) (
	ret []krpc.NodeInfo,
) {
	for _, n := range s.closestNodes(k, targetID, func(n *node) bool {
		return n.IsGood() && filter(n.NodeInfo().Addr)
	}) {
		ret = append(ret, n.NodeInfo())
	}
	return
}

func (s *Server) closestNodes(k int, target int160, filter func(*node) bool) []*node {
	return s.table.closestNodes(k, target, filter)
}

func (s *Server) traversalStartingNodes() (nodes []addrMaybeId, err error) {
	s.mu.RLock()
	s.table.forNodes(func(n *node) bool {
		nodes = append(nodes, addrMaybeId{n.addr.KRPC(), &n.id})
		return true
	})
	s.mu.RUnlock()
	if len(nodes) > 0 {
		return
	}
	if s.config.StartingNodes != nil {
		addrs, err := s.config.StartingNodes()
		if err != nil {
			return nil, errors.Wrap(err, "getting starting nodes")
		}
		for _, a := range addrs {
			nodes = append(nodes, addrMaybeId{a.KRPC(), nil})
		}
	}
	if len(nodes) == 0 {
		err = errors.New("no initial nodes")
	}
	return
}

func (s *Server) AddNodesFromFile(fileName string) (added int, err error) {
	ns, err := ReadNodesFromFile(fileName)
	if err != nil {
		return
	}
	for _, n := range ns {
		if s.AddNode(n) == nil {
			added++
		}
	}
	return
}

func (s *Server) logger() *log.Logger {
	return s.config.Logger
}
