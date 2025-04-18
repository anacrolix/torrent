package dht

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2"
	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/iplist"
	"github.com/james-lawrence/torrent/logonce"

	"github.com/james-lawrence/torrent/dht/bep44"
	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/dht/krpc"
	peer_store "github.com/james-lawrence/torrent/dht/peer-store"
	"github.com/james-lawrence/torrent/dht/transactions"
	"github.com/james-lawrence/torrent/dht/traversal"
	"github.com/james-lawrence/torrent/dht/types"
	"github.com/james-lawrence/torrent/internal/langx"
)

// A Server defines parameters for a DHT node server that is able to send
// queries, and respond to the ones from the network. Each node has a globally
// unique identifier known as the "node ID." Node IDs are chosen at random
// from the same 160-bit space as BitTorrent infohashes and define the
// behaviour of the node. Zero valued Server does not have a valid ID and thus
// is unable to function properly. Use `NewServer(nil)` to initialize a
// default node.
type Server struct {
	id          int160.T
	socket      net.PacketConn
	resendDelay func() time.Duration

	mu           sync.RWMutex
	transactions transactions.Dispatcher[*transaction]
	table        table
	closed       missinggo.Event
	ipBlockList  iplist.Ranger
	tokenServer  tokenServer // Manages tokens we issue to our queriers.
	config       ServerConfig
	stats        ServerStats

	lastBootstrap    time.Time
	bootstrappingNow bool
	mux              Muxer
	store            *bep44.Wrapper
}

func (s *Server) numGoodNodes() (num int) {
	s.table.forNodes(func(n *node) bool {
		if s.IsGood(n) {
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
	fmt.Fprintf(w, "Ongoing transactions: %d\n", s.transactions.NumActive())
	fmt.Fprintf(w, "Server node ID: %x\n", s.id.Bytes())
	buckets := &s.table.buckets
	for i := range s.table.buckets {
		b := &buckets[i]
		if b.Len() == 0 && b.lastChanged.IsZero() {
			continue
		}
		fmt.Fprintf(w,
			"b# %v: %v nodes, last updated: %v\n",
			i, b.Len(), prettySince(b.lastChanged))
		if b.Len() > 0 {
			tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
			fmt.Fprintf(tw, "  node id\taddr\tlast query\tlast response\trecv\tdiscard\tflags\n")
			// Bucket nodes ordered by distance from server ID.
			nodes := slices.SortedFunc(b.NodeIter(), func(l *node, r *node) int {
				return l.Id.Distance(s.id).Cmp(r.Id.Distance(s.id))
			})
			for _, n := range nodes {
				var flags []string
				if s.IsQuestionable(n) {
					flags = append(flags, "q10e")
				}
				if s.nodeIsBad(n) {
					flags = append(flags, "bad")
				}
				if s.IsGood(n) {
					flags = append(flags, "good")
				}
				if n.IsSecure() {
					flags = append(flags, "sec")
				}
				fmt.Fprintf(tw, "  %x\t%s\t%s\t%s\t%d\t%v\t%v\n",
					n.Id.Bytes(),
					n.Addr,
					prettySince(n.lastGotQuery),
					prettySince(n.lastGotResponse),
					n.numReceivesFrom,
					n.failedLastQuestionablePing,
					strings.Join(flags, ","),
				)
			}
			tw.Flush()
		}
	}
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
	ss.OutstandingTransactions = s.transactions.NumActive()
	return ss
}

// Addr returns the listen address for the server. Packets arriving to this address
// are processed by the server (unless aliens are involved).
func (s *Server) Addr() net.Addr {
	return s.socket.LocalAddr()
}

func NewDefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		NoSecurity:    true,
		StartingNodes: func() ([]Addr, error) { return GlobalBootstrapAddrs("udp") },
		DefaultWant:   []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
		Store:         bep44.NewMemory(),
		Exp:           2 * time.Hour,
		SendLimiter:   DefaultSendLimiter,
		Logger:        log.Default(),
		BucketLimit:   8,
	}
}

// If the NodeId hasn't been specified, generate a suitable one. deterministic if c.Conn and
// c.PublicIP are non-nil.
func (c *ServerConfig) InitNodeId() (deterministic bool) {
	if c.NodeId.IsZero() {
		var secure bool
		if c.Conn != nil && c.PublicIP != nil {
			// Is this sufficient for a deterministic node ID?
			c.NodeId = HashTuple(
				[]byte(c.Conn.LocalAddr().Network()),
				[]byte(c.Conn.LocalAddr().String()),
				c.PublicIP,
			)
			// Since we have a public IP we can secure, and the choice must not be influenced by the
			// NoSecure configuration option.
			secure = true
			deterministic = true
		} else {
			c.NodeId = krpc.RandomID()
			secure = !c.NoSecurity && c.PublicIP != nil
		}
		if secure {
			SecureNodeId(&c.NodeId, c.PublicIP)
		}
	}
	return
}

// NewServer initializes a new DHT node server.
func NewServer(c *ServerConfig) (s *Server, err error) {
	if c == nil {
		c = NewDefaultServerConfig()
	}
	if c.Conn == nil {
		c.Conn, err = net.ListenPacket("udp", ":0")
		if err != nil {
			return
		}
	}
	c.InitNodeId()

	if c.Store == nil {
		c.Store = bep44.NewMemory()
	}
	if c.SendLimiter == nil {
		c.SendLimiter = DefaultSendLimiter
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	if c.QueryResendDelay == nil {
		c.QueryResendDelay = func() time.Duration { return 2 * time.Second }
	}

	s = &Server{
		config:      *c,
		ipBlockList: c.IPBlocklist,
		tokenServer: tokenServer{
			maxIntervalDelta: 2,
			interval:         5 * time.Minute,
			secret:           make([]byte, 20),
		},
		table: table{
			k: c.BucketLimit,
			m: &sync.Mutex{},
		},
		store: bep44.NewWrapper(c.Store, c.Exp),
		mux:   DefaultMuxer(),
	}
	rand.Read(s.tokenServer.secret)
	s.socket = c.Conn
	s.id = int160.FromByteArray(c.NodeId)
	s.table.rootID = s.id
	s.resendDelay = s.config.QueryResendDelay
	if s.resendDelay == nil {
		s.resendDelay = defaultQueryResendDelay
	}

	go s.serveUntilClosed()
	return
}

func (s *Server) serveUntilClosed() error {
	err := s.serve()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.IsSet() {
		return nil
	}
	return err
}

// Returns a description of the Server.
func (s *Server) String() string {
	return fmt.Sprintf("dht server on %s (node id %v)", s.socket.LocalAddr(), s.id)
}

// Packets to and from any address matching a range in the list are dropped.
func (s *Server) SetIPBlockList(list iplist.Ranger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ipBlockList = list
}

func (s *Server) IPBlocklist() iplist.Ranger {
	return s.ipBlockList
}

func (s *Server) processPacket(ctx context.Context, b []byte, addr Addr) {
	// log.Printf("got packet %q", b)
	if len(b) < 2 || b[0] != 'd' {
		// KRPC messages are bencoded dicts.
		readNotKRPCDict.Add(1)
		return
	}
	var d krpc.Msg
	err := bencode.Unmarshal(b, &d)
	if _, ok := err.(bencode.ErrUnusedTrailingBytes); ok {
		// log.Printf("%s: received message packet with %d trailing bytes: %q", s, _err.NumUnusedBytes, b[len(b)-_err.NumUnusedBytes:])
		expvars.Add("processed packets with trailing bytes", 1)
	} else if err != nil {
		readUnmarshalError.Add(1)
		// log.Printf("%s: received bad krpc message from %s: %s: %+q", s, addr, err, b)
		func() {
			if se, ok := err.(*bencode.SyntaxError); ok {
				// The message was truncated.
				if int(se.Offset) == len(b) {
					return
				}
				// Some messages seem to drop to nul chars abruptly.
				if int(se.Offset) < len(b) && b[se.Offset] == 0 {
					return
				}
				// The message isn't bencode from the first.
				if se.Offset == 0 {
					return
				}
			}
			// if missinggo.CryHeard() {
			log.Printf("%s: received bad krpc message from %s: %s: %+q", s, addr, err, b)
			// }
		}()
		return
	}

	if s.closed.IsSet() {
		return
	}

	if d.Y == "q" {
		s.logger().Printf("received query %q from %v", d.Q, addr)
		s.handleQuery(ctx, addr, b, d)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tk := transactionKey{
		RemoteAddr: addr.String(),
		T:          d.T,
	}
	if !s.transactions.Have(tk) {
		s.logger().Printf("received response for untracked transaction %q from %v", d.T, addr)
		return
	}
	t := s.transactions.Pop(tk)

	// s.logger().Printf("received response for transaction %q from %v", d.T, addr)
	go t.handleResponse(b, d)
	s.updateNode(addr, d.SenderID(), !d.ReadOnly, func(n *node) {
		n.lastGotResponse = time.Now()
		n.failedLastQuestionablePing = false
		n.numReceivesFrom++
	})
}

func (s *Server) serve() error {
	var b [0x10000]byte
	for {
		n, addr, err := s.socket.ReadFrom(b[:])
		if err != nil {
			if ignoreReadFromError(err) {
				continue
			}
			return err
		}
		expvars.Add("packets read", 1)
		if n == len(b) {
			logonce.Stderr.Printf("received dht packet exceeds buffer size")
			continue
		}
		if missinggo.AddrPort(addr) == 0 {
			readZeroPort.Add(1)
			continue
		}
		blocked, err := func() (bool, error) {
			s.mu.RLock()
			defer s.mu.RUnlock()
			if s.closed.IsSet() {
				return false, errors.New("server is closed")
			}
			return s.ipBlocked(missinggo.AddrIP(addr)), nil
		}()
		if err != nil {
			return err
		}
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
func (s *Server) AddNode(nis ...krpc.NodeInfo) error {
	addnode := func(n krpc.NodeInfo) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.updateNode(NewAddr(n.Addr.UDP()), (*krpc.ID)(&n.ID), true, func(*node) {})
	}
	for _, ni := range nis {
		id := int160.FromByteArray(ni.ID)
		if id.IsZero() {
			go s.Ping(ni.Addr.UDP())
			return nil
		}

		if err := addnode(ni); err != nil {
			return err
		}
	}

	return nil
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
	// Is it possible to be over IPv6 with IPv4 endpoints?
	return querySource.To4() != nil
}

func shouldReturnNodes6(queryWants []krpc.Want, querySource net.IP) bool {
	if len(queryWants) != 0 {
		return wantsContain(queryWants, krpc.WantNodes6)
	}
	return querySource.To4() == nil
}

func (s *Server) MakeReturnNodes(target int160.T, filter func(krpc.NodeAddr) bool) []krpc.NodeInfo {
	return s.closestGoodNodeInfos(8, target, filter)
}

var krpcErrMissingArguments = krpc.Error{
	Code: krpc.ErrorCodeProtocolError,
	Msg:  "missing arguments dict",
}

// Filters peers per BEP 32 to return in the values field to a get_peers query.
func filterPeers(querySourceIp net.IP, queryWants []krpc.Want, allPeers []krpc.NodeAddr) (filtered []krpc.NodeAddr) {
	// The logic here is common with nodes, see BEP 32.
	retain4 := shouldReturnNodes(queryWants, querySourceIp)
	retain6 := shouldReturnNodes6(queryWants, querySourceIp)
	for _, peer := range allPeers {
		if ip, ok := func(ip net.IP) (net.IP, bool) {
			as4 := peer.IP().To4()
			as16 := peer.IP().To16()
			switch {
			case retain4 && len(ip) == net.IPv4len:
				return ip, true
			case retain6 && len(ip) == net.IPv6len:
				return ip, true
			case retain4 && as4 != nil:
				// Is it possible that we're converting to an IPv4 address when the transport in use
				// is IPv6?
				return as4, true
			case retain6 && as16 != nil:
				// Couldn't any IPv4 address be converted to IPv6, but isn't listening over IPv6?
				return as16, true
			default:
				return nil, false
			}
		}(peer.IP()); ok {
			filtered = append(filtered, krpc.NewNodeAddrFromIPPort(ip, int(peer.Port())))
		}
	}
	return
}

func (s *Server) setReturnNodes(r *krpc.Return, queryMsg krpc.Msg, querySource Addr) *krpc.Error {
	if queryMsg.A == nil {
		return &krpcErrMissingArguments
	}
	target := int160.FromByteArray(queryMsg.A.InfoHash)
	if shouldReturnNodes(queryMsg.A.Want, querySource.IP()) {
		r.Nodes = s.MakeReturnNodes(target, func(na krpc.NodeAddr) bool { return na.Addr().Is4() })
	}
	if shouldReturnNodes6(queryMsg.A.Want, querySource.IP()) {
		r.Nodes6 = s.MakeReturnNodes(target, func(krpc.NodeAddr) bool { return true })
	}
	return nil
}

func (s *Server) ServeMux(ctx context.Context, c net.PacketConn, m Muxer) error {
	s.mu.Lock()
	s.mux = m
	s.socket = c
	s.mu.Unlock()

	return s.serveUntilClosed()
}

func (s *Server) handleQuery(ctx context.Context, source Addr, raw []byte, m krpc.Msg) {
	var (
		pattern string
		fn      Handler
	)

	s.updateNode(source, m.SenderID(), !m.ReadOnly, func(n *node) {
		n.lastGotQuery = time.Now()
		n.numReceivesFrom++
	})

	if s.config.OnQuery != nil {
		propagate := s.config.OnQuery(&m, source.Raw())
		if !propagate {
			return
		}
	}

	if pattern, fn = s.mux.Handler(raw, &m); fn == nil {
		log.Println("unable to locate a handler for", pattern)
		return
	}

	if err := fn.Handle(ctx, source, s, raw, &m); err != nil {
		log.Println("query failed", source.String(), err)
		if cause, ok := err.(*krpc.Error); ok {
			if err := s.sendError(ctx, source, m.T, *cause); err != nil {
				log.Println("unable to return an error", err)
			}
		}
	}
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
	_, err = s.SendToNode(ctx, b, addr, 1)
	if err != nil {
		s.logger().Printf("error replying to %q: %v", addr, err)
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
	b := bencode.MustMarshal(m)
	s.logger().Printf("replying to %q\n", addr)
	_, err := s.SendToNode(ctx, b, addr, 1)
	if err != nil {
		s.logger().Printf("error replying to %s: %s\n", addr, err)
		return err
	}

	return nil
}

// Adds a node if appropriate.
func (s *Server) addNode(n *node) error {
	if s.nodeIsBad(n) {
		return errors.New("node is bad")
	}
	b := s.table.bucketForID(n.Id)
	if b.Len() >= s.table.k {
		if b.EachNode(func(bn *node) bool {
			// Replace bad and untested nodes with a good one.
			if s.nodeIsBad(bn) || (s.IsGood(n) && bn.lastGotResponse.IsZero()) {
				s.table.dropNode(bn)
			}
			return b.Len() >= s.table.k
		}) {
			return errors.New("no room in bucket")
		}
	}

	if err := s.table.addNode(n); err != nil {
		return fmt.Errorf("expected to add node: %s", err)
	}

	return nil
}

func (s *Server) NodeRespondedToPing(addr Addr, id int160.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.id {
		return
	}
	b := s.table.bucketForID(id)
	if b.GetNode(addr, id) == nil {
		return
	}
	b.lastChanged = time.Now()
}

// Updates the node, adding it if appropriate.
func (s *Server) updateNode(addr Addr, id *krpc.ID, tryAdd bool, update func(*node)) error {
	if id == nil {
		return errors.New("id is nil")
	}
	int160Id := int160.FromByteArray(*id)
	n := s.table.getNode(addr, int160Id)
	missing := n == nil
	if missing {
		if !tryAdd {
			return errors.New("node not present and add flag false")
		}
		if int160Id == s.id {
			return errors.New("can't store own id in routing table")
		}
		n = &node{nodeKey: nodeKey{
			Id:   int160Id,
			Addr: addr,
		}}
	}
	update(n)
	if !missing {
		return nil
	}
	return s.addNode(n)
}

func (s *Server) nodeIsBad(n *node) bool {
	return s.nodeErr(n) != nil
}

func (s *Server) nodeErr(n *node) error {
	if n.Id == s.id {
		return errors.New("is self")
	}
	if n.Id.IsZero() {
		return errors.New("has zero id")
	}
	if !(s.config.NoSecurity || n.IsSecure()) {
		return errors.New("not secure")
	}
	if n.failedLastQuestionablePing {
		return errors.New("didn't respond to last questionable node ping")
	}
	return nil
}

func (s *Server) SendToNode(ctx context.Context, b []byte, node Addr, maximum int) (wrote bool, err error) {
	func() {
		// This is a pain. It would be better if the blocklist returned an error if it was closed
		// instead.
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.closed.IsSet() {
			err = errors.New("server is closed")
			return
		}
		if list := s.ipBlockList; list != nil {
			if r, ok := list.Lookup(node.IP()); ok {
				err = fmt.Errorf("write to %v blocked by %v", node, r)
				return
			}
		}
	}()

	if err != nil {
		return false, err
	}

	// n, err := s.socket.WriteTo(b, node.Raw())
	n, err := repeatsend(ctx, s.socket, node.Raw(), b, s.config.QueryResendDelay(), maximum)
	if err != nil {
		return false, err
	}

	wrote = true
	if n != len(b) {
		return wrote, io.ErrShortWrite
	}

	return wrote, nil
}

func (s *Server) deleteTransaction(k transactionKey) {
	if s.transactions.Have(k) {
		s.transactions.Pop(k)
	}
}

func (s *Server) addTransaction(k transactionKey, t *transaction) {
	s.transactions.Add(k, t)
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

type numWrites int

type QueryResult struct {
	Raw    []byte
	Reply  krpc.Msg
	Writes numWrites
	Err    error
}

func (qr QueryResult) ToError() error {
	if qr.Err != nil {
		return qr.Err
	}

	return nil
}

// Converts a Server QueryResult to a traversal.QueryResult.
func (me QueryResult) TraversalQueryResult(addr krpc.NodeAddr) (ret traversal.QueryResult) {
	r := me.Reply.R
	if r == nil {
		return
	}
	ret.ResponseFrom = &krpc.NodeInfo{
		Addr: addr,
		ID:   r.ID,
	}
	ret.Nodes = r.Nodes
	ret.Nodes6 = r.Nodes6
	if r.Token != nil {
		ret.ClosestData = *r.Token
	}
	return
}

// Rate-limiting to be applied to writes for a given query. Queries occur inside transactions that
// will attempt to send several times. If the STM rate-limiting helpers are used, the first send is
// often already accounted for in the rate-limiting machinery before the query method that does the
// IO is invoked.
type QueryRateLimiting struct {
	// Don't rate-limit the first send for a query.
	NotFirst bool
	// Don't rate-limit any sends for a query. Note that there's still built-in waits before retries.
	NotAny        bool
	WaitOnRetries bool
	NoWaitFirst   bool
}

// The zero value for this uses reasonable/traditional defaults on Server methods.
type QueryInput struct {
	Method   string
	Tid      string
	Encoded  []byte
	NumTries int
}

// Performs an arbitrary query. `q` is the query value, defined by the DHT BEP. `a` should contain
// the appropriate argument values, if any. `a.ID` is clobbered by the Server. Responses to queries
// made this way are not interpreted by the Server. More specific methods like FindNode and GetPeers
// may make use of the response internally before passing it back to the caller.
func (s *Server) Query(ctx context.Context, addr Addr, input QueryInput) (ret QueryResult) {
	defer func(started time.Time) {
		s.logger().Printf(
			"Query(%v) returned after %v (err=%v, reply.Y=%v, reply.E=%v, writes=%v)",
			input.Method, time.Since(started), ret.Err, ret.Reply.Y, ret.Reply.E, ret.Writes)
	}(time.Now())

	replyChan := make(chan *QueryResult, 1)
	sctx, done := context.WithCancelCause(pprof.WithLabels(ctx, pprof.Labels("q", input.Method)))
	// Make sure the query sender stops.
	defer done(nil)

	t := &transaction{
		onResponse: func(m []byte, r krpc.Msg) {
			select {
			case replyChan <- &QueryResult{
				Raw:   m,
				Reply: r,
				Err:   r.Error(),
			}:
			case <-sctx.Done():
			}
		},
	}
	tk := transactionKey{
		RemoteAddr: addr.String(),
	}
	s.mu.Lock()
	s.stats.OutboundQueriesAttempted++
	tk.T = input.Tid
	s.addTransaction(tk, t)
	s.mu.Unlock()

	go func() {
		s.logger().Printf("transmitting initiated %s %q %d\n", input.Method, input.Tid, input.NumTries)
		_, err := s.SendToNode(sctx, input.Encoded, addr, input.NumTries)
		s.logger().Printf("transmitting completed %s %q %d %v\n", input.Method, input.Tid, input.NumTries, err)
		if err != nil {
			done(err)
		}
	}()

	defer func() {
		s.mu.Lock()
		s.deleteTransaction(tk)
		s.mu.Unlock()
	}()

	select {
	case qr := <-replyChan:
		return *qr
	case <-sctx.Done():
		return NewQueryResultErr(errorsx.Compact(context.Cause(sctx), sctx.Err()))
	}
}

// Sends a ping query to the address given.
func (s *Server) PingQueryInput(node net.Addr, qi QueryInput) QueryResult {
	addr := NewAddr(node)
	res := Ping3S(context.Background(), s, addr, s.ID())
	if res.Err == nil {
		id := res.Reply.SenderID()
		if id != nil {
			s.NodeRespondedToPing(addr, id.Int160())
		}
	}

	return res
}

// Sends a ping query to the address given.
func (s *Server) Ping(node net.Addr) QueryResult {
	return s.PingQueryInput(node, QueryInput{})
}

// Put adds a new item to node. You need to call Get first for a write token.
func (s *Server) Put(ctx context.Context, node Addr, i bep44.Put, token string, rl QueryRateLimiting) QueryResult {
	if err := s.store.Put(i.ToItem()); err != nil {
		return QueryResult{
			Err: err,
		}
	}

	qi, err := NewMessageRequest("put", s.ID(), &krpc.MsgArgs{
		Cas:   i.Cas,
		ID:    s.ID(),
		Salt:  i.Salt,
		Seq:   &i.Seq,
		Sig:   i.Sig,
		Token: token,
		V:     i.V,
		K:     langx.Autoderef(i.K),
	})

	if err != nil {
		return QueryResult{Err: err}
	}

	return s.Query(ctx, node, qi)
}

func (s *Server) announcePeer(
	ctx context.Context,
	node Addr, infoHash int160.T, port int, token string, impliedPort bool,
) (
	ret QueryResult,
) {

	qi, err := NewAnnouncePeerRequest(s.ID(), infoHash.AsByteArray(), port, token, impliedPort)
	if err != nil {
		return NewQueryResultErr(err)
	}

	if ret = s.Query(ctx, node, qi); ret.Err != nil {
		return ret
	}

	if ret.Err != nil {
		return ret
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.SuccessfulOutboundAnnouncePeerQueries++
	return
}

// Sends a find_node query to addr. targetID is the node we're looking for. The Server makes use of
// some of the response fields.
func (s *Server) FindNode(addr Addr, targetID int160.T, rl QueryRateLimiting) (ret QueryResult) {
	return FindNode(context.Background(), s, addr, s.ID(), targetID, s.config.DefaultWant)
}

// Returns how many nodes are in the node table.
func (s *Server) NumNodes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.numNodes()
}

// Returns non-bad nodes from the routing table.
func (s *Server) Nodes() (nis []krpc.NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notBadNodes()
}

// Returns non-bad nodes from the routing table.
func (s *Server) notBadNodes() (nis []krpc.NodeInfo) {
	s.table.forNodes(func(n *node) bool {
		if s.nodeIsBad(n) {
			return true
		}
		nis = append(nis, krpc.NodeInfo{
			Addr: n.Addr.KRPC(),
			ID:   n.Id.AsByteArray(),
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
	go s.socket.Close()
}

func (s *Server) GetPeers(
	ctx context.Context,
	addr Addr,
	infoHash int160.T,
	// Be advised that if you set this, you might not get any "Return.values" back. That wasn't my
	// reading of BEP 33 but there you go.
	scrape bool,
	rl QueryRateLimiting,
) (ret QueryResult) {
	qi, err := NewPeersRequest(s.ID(), infoHash.AsByteArray(), scrape)
	if err != nil {
		return NewQueryResultErr(err)
	}

	ret = s.Query(ctx, addr, qi)
	return ret
}

// Get gets item information from a specific target ID. If seq is set to a specific value,
// only items with seq bigger than the one provided will return a V, K and Sig, if any.
// Get must be used to get a Put write token, when you want to write an item instead of read it.
func (s *Server) Get(ctx context.Context, addr Addr, target bep44.Target, seq *int64, rl QueryRateLimiting) QueryResult {
	qi, err := NewMessageRequest("get", s.ID(), &krpc.MsgArgs{
		ID:     s.ID(),
		Target: target,
		Seq:    seq,
		Want:   []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
	})
	if err != nil {
		return NewQueryResultErr(err)
	}

	return s.Query(ctx, addr, qi)
}

func (s *Server) ClosestGoodNodeInfos(
	k int,
	targetID int160.T,
) (
	ret []krpc.NodeInfo,
) {
	return s.closestGoodNodeInfos(k, targetID, func(na krpc.NodeAddr) bool { return true })
}

func (s *Server) closestGoodNodeInfos(
	k int,
	targetID int160.T,
	filter func(krpc.NodeAddr) bool,
) (
	ret []krpc.NodeInfo,
) {
	for _, n := range s.closestNodes(k, targetID, func(n *node) bool {
		return s.IsGood(n) && filter(n.NodeInfo().Addr)
	}) {
		ret = append(ret, n.NodeInfo())
	}
	return
}

func (s *Server) closestNodes(k int, target int160.T, filter func(*node) bool) []*node {
	return s.table.closestNodes(k, target, filter)
}

func (s *Server) TraversalStartingNodes() (nodes []addrMaybeId, err error) {
	s.mu.RLock()
	s.table.forNodes(func(n *node) bool {
		nodes = append(nodes, addrMaybeId{
			Addr: n.Addr.KRPC(),
			Id:   generics.Some(n.Id)})
		return true
	})
	s.mu.RUnlock()
	if len(nodes) > 0 {
		return
	}
	if s.config.StartingNodes != nil {
		// There seems to be floods on this call on occasion, which may cause a barrage of DNS
		// resolution attempts. This would require that we're unable to get replies because we can't
		// resolve, transmit or receive on the network. Nodes currently don't get expired from the
		// table, so once we have some entries, we should never have to fallback.
		// s.logger().Println("falling back on starting nodes")
		addrs, err := s.config.StartingNodes()
		if err != nil {
			return nil, fmt.Errorf("getting starting nodes: %w", err)
		} else {
			// log.Printf("resolved %v addresses", len(addrs))
		}
		for _, a := range addrs {
			nodes = append(nodes, addrMaybeId{Addr: a.KRPC()})
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
	if s.config.Logger == nil {
		panic("missing logger")
	}
	return s.config.Logger
}

func (s *Server) PeerStore() peer_store.Interface {
	return s.config.PeerStore
}

func (s *Server) shouldStopRefreshingBucket(bucketIndex int) bool {
	if s.closed.IsSet() {
		return true
	}
	b := &s.table.buckets[bucketIndex]
	// Stop if the bucket is full, and none of the nodes are bad.
	return b.Len() == s.table.K() && b.EachNode(func(n *node) bool {
		return !s.nodeIsBad(n)
	})
}

func (s *Server) refreshBucket(bucketIndex int) *traversal.Stats {
	s.mu.RLock()
	id := s.table.randomIdForBucket(bucketIndex)
	op := traversal.Start(traversal.OperationInput{
		Target: id.AsByteArray(),
		Alpha:  3,
		// Running this to completion with K matching the full-bucket size should result in a good,
		// full bucket, since the Server will add nodes that respond to its table to replace the bad
		// ones we're presumably refreshing. It might be possible to terminate the traversal early
		// as soon as the bucket is good.
		K: s.table.K(),
		DoQuery: func(ctx context.Context, addr krpc.NodeAddr) traversal.QueryResult {
			res := s.FindNode(NewAddr(addr.UDP()), id, QueryRateLimiting{})
			err := res.Err
			if err != nil && !errors.Is(err, ErrTransactionTimeout) {
				s.logger().Printf("error doing find node while refreshing bucket: %v\n", err)
			}
			return res.TraversalQueryResult(addr)
		},
		NodeFilter: s.TraversalNodeFilter,
	})
	defer func() {
		s.mu.RUnlock()
		op.Stop()
		<-op.Stopped()
	}()
	b := &s.table.buckets[bucketIndex]
wait:
	for {
		if s.shouldStopRefreshingBucket(bucketIndex) {
			break wait
		}
		op.AddNodes(types.AddrMaybeIdSliceFromNodeInfoSlice(s.notBadNodes()))
		bucketChanged := b.changed.Signaled()
		serverClosed := s.closed.C()
		s.mu.RUnlock()
		select {
		case <-op.Stalled():
			s.mu.RLock()
			break wait
		case <-bucketChanged:
		case <-serverClosed:
		}
		s.mu.RLock()
	}
	return op.Stats()
}

func (s *Server) shouldBootstrap() bool {
	return s.lastBootstrap.IsZero() || time.Since(s.lastBootstrap) > 30*time.Minute
}

func (s *Server) shouldBootstrapUnlocked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shouldBootstrap()
}

func (s *Server) pingQuestionableNodesInBucket(bucketIndex int) {
	b := &s.table.buckets[bucketIndex]
	var wg sync.WaitGroup
	b.EachNode(func(n *node) bool {
		if s.IsQuestionable(n) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.questionableNodePing(context.TODO(), n.Addr, n.Id.AsByteArray()).Err
				if err != nil {
					s.logger().Printf("error pinging questionable node in bucket %v: %v", bucketIndex, err)
				}
			}()
		}
		return true
	})
	s.mu.RUnlock()
	wg.Wait()
	s.mu.RLock()
}

// A routine that maintains the Server's routing table, by pinging questionable nodes, and
// refreshing buckets. This should be invoked on a running Server when the caller is satisfied with
// having set it up. It is not necessary to explicitly Bootstrap the Server once this routine has
// started.
func (s *Server) TableMaintainer() {
	logger := s.logger()
	for {
		if s.shouldBootstrapUnlocked() {
			stats, err := s.Bootstrap(context.Background())
			if err != nil {
				logger.Printf("error bootstrapping during bucket refresh: %v\n", err)
			}
			logger.Printf("bucket refresh bootstrap stats: %v\n", stats)
		}
		s.mu.RLock()
		for i := range s.table.buckets {
			s.pingQuestionableNodesInBucket(i)
			// if time.Since(b.lastChanged) < 15*time.Minute {
			//	continue
			// }
			if s.shouldStopRefreshingBucket(i) {
				continue
			}
			logger.Printf("refreshing bucket %v\n", i)
			s.mu.RUnlock()
			stats := s.refreshBucket(i)
			logger.Printf("finished refreshing bucket %v: %v\n", i, stats)
			s.mu.RLock()
			if !s.shouldStopRefreshingBucket(i) {
				// Presumably we couldn't fill the bucket anymore, so assume we're as deep in the
				// available node space as we can go.
				break
			}
		}
		s.mu.RUnlock()
		select {
		case <-s.closed.LockedChan(&s.mu):
			return
		case <-time.After(time.Minute):
		}
	}
}

func (s *Server) questionableNodePing(ctx context.Context, addr Addr, id krpc.ID) QueryResult {
	qi, err := NewPingRequest(s.ID())
	if err != nil {
		return NewQueryResultErr(err)
	}

	// A ping query that will be certain to try at least 3 times.
	qi.NumTries = 3

	res := s.Query(ctx, addr, qi)
	if res.Err == nil && res.Reply.R != nil {
		s.NodeRespondedToPing(addr, res.Reply.R.ID.Int160())
	} else {
		s.mu.Lock()
		s.updateNode(addr, &id, false, func(n *node) {
			n.failedLastQuestionablePing = true
		})
		s.mu.Unlock()
	}
	return res
}

// Whether we should consider a node for contact based on its address and possible ID.
func (s *Server) TraversalNodeFilter(node addrMaybeId) bool {
	if !validNodeAddr(node.Addr.UDP()) {
		return false
	}
	if s.ipBlocked(node.Addr.IP()) {
		return false
	}
	if !node.Id.Ok {
		return true
	}
	return s.config.NoSecurity || NodeIdSecure(node.Id.Value.AsByteArray(), node.Addr.IP())
}

func validNodeAddr(addr net.Addr) bool {
	// At least for UDP addresses, we know what doesn't work.
	ua := addr.(*net.UDPAddr)
	if ua.Port == 0 {
		return false
	}
	if ip4 := ua.IP.To4(); ip4 != nil && ip4[0] == 0 {
		// Why?
		return false
	}
	return true
}
