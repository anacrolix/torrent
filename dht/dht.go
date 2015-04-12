// Package DHT implements a DHT for use with the BitTorrent protocol,
// described in BEP 5: http://www.bittorrent.org/beps/bep_0005.html.
//
// Standard use involves creating a NewServer, and calling Announce on it with
// the details of your local torrent client and infohash of interest.
package dht

import (
	"crypto"
	_ "crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/anacrolix/libtorgo/bencode"
	"github.com/anacrolix/sync"

	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/logonce"
	"github.com/anacrolix/torrent/util"
)

const (
	maxNodes         = 320
	queryResendEvery = 5 * time.Second
)

// Uniquely identifies a transaction to us.
type transactionKey struct {
	RemoteAddr string // host:port
	T          string // The KRPC transaction ID.
}

type Server struct {
	id               string
	socket           net.PacketConn
	transactions     map[transactionKey]*Transaction
	transactionIDInt uint64
	nodes            map[string]*node // Keyed by dHTAddr.String().
	mu               sync.Mutex
	closed           chan struct{}
	passive          bool // Don't respond to queries.
	ipBlockList      *iplist.IPList

	numConfirmedAnnounces int
	bootstrapNodes        []string
}

type dHTAddr interface {
	net.Addr
	UDPAddr() *net.UDPAddr
}

type cachedAddr struct {
	a net.Addr
	s string
}

func (ca cachedAddr) Network() string {
	return ca.a.Network()
}

func (ca cachedAddr) String() string {
	return ca.s
}

func (ca cachedAddr) UDPAddr() *net.UDPAddr {
	return ca.a.(*net.UDPAddr)
}

func newDHTAddr(addr net.Addr) dHTAddr {
	return cachedAddr{addr, addr.String()}
}

type ServerConfig struct {
	Addr string // Listen address. Used if Conn is nil.
	Conn net.PacketConn
	// Don't respond to queries from other nodes.
	Passive bool
	// DHT Bootstrap nodes
	BootstrapNodes []string
}

type ServerStats struct {
	// Count of nodes in the node table that responded to our last query or
	// haven't yet been queried.
	GoodNodes int
	// Count of nodes in the node table.
	Nodes int
	// Transactions awaiting a response.
	OutstandingTransactions int
	// Individual announce_peer requests that got a success response.
	ConfirmedAnnounces int
}

// Returns statistics for the server.
func (s *Server) Stats() (ss ServerStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nodes {
		if n.DefinitelyGood() {
			ss.GoodNodes++
		}
	}
	ss.Nodes = len(s.nodes)
	ss.OutstandingTransactions = len(s.transactions)
	ss.ConfirmedAnnounces = s.numConfirmedAnnounces
	return
}

// Returns the listen address for the server. Packets arriving to this address
// are processed by the server (unless aliens are involved).
func (s *Server) Addr() net.Addr {
	return s.socket.LocalAddr()
}

func makeSocket(addr string) (socket *net.UDPConn, err error) {
	addr_, err := net.ResolveUDPAddr("", addr)
	if err != nil {
		return
	}
	socket, err = net.ListenUDP("udp", addr_)
	return
}

// Create a new DHT server.
func NewServer(c *ServerConfig) (s *Server, err error) {
	if c == nil {
		c = &ServerConfig{}
	}
	s = &Server{}
	if c.Conn != nil {
		s.socket = c.Conn
	} else {
		s.socket, err = makeSocket(c.Addr)
		if err != nil {
			return
		}
	}
	s.passive = c.Passive
	s.bootstrapNodes = c.BootstrapNodes
	err = s.init()
	if err != nil {
		return
	}

	return
}

// Returns a description of the Server. Python repr-style.
func (s *Server) String() string {
	return fmt.Sprintf("dht server on %s", s.socket.LocalAddr())
}

type nodeID struct {
	i   big.Int
	set bool
}

func (nid *nodeID) IsUnset() bool {
	return !nid.set
}

func nodeIDFromString(s string) (ret nodeID) {
	if s == "" {
		return
	}
	ret.i.SetBytes([]byte(s))
	ret.set = true
	return
}

func (nid0 *nodeID) Distance(nid1 *nodeID) (ret big.Int) {
	if nid0.IsUnset() != nid1.IsUnset() {
		ret = maxDistance
		return
	}
	ret.Xor(&nid0.i, &nid1.i)
	return
}

func (nid *nodeID) String() string {
	return string(nid.i.Bytes())
}

type node struct {
	addr          dHTAddr
	id            nodeID
	announceToken string

	lastGotQuery    time.Time
	lastGotResponse time.Time
	lastSentQuery   time.Time
}

func (n *node) idString() string {
	return n.id.String()
}

func (n *node) SetIDFromBytes(b []byte) {
	n.id.i.SetBytes(b)
	n.id.set = true
}

func (n *node) SetIDFromString(s string) {
	n.id.i.SetBytes([]byte(s))
}

func (n *node) IDNotSet() bool {
	return n.id.i.Int64() == 0
}

func (n *node) NodeInfo() (ret NodeInfo) {
	ret.Addr = n.addr
	if n := copy(ret.ID[:], n.idString()); n != 20 {
		panic(n)
	}
	return
}

func (n *node) DefinitelyGood() bool {
	if len(n.idString()) != 20 {
		return false
	}
	// No reason to think ill of them if they've never been queried.
	if n.lastSentQuery.IsZero() {
		return true
	}
	// They answered our last query.
	if n.lastSentQuery.Before(n.lastGotResponse) {
		return true
	}
	return true
}

// A wrapper around the unmarshalled KRPC dict that constitutes messages in
// the DHT. There are various helpers for extracting common data from the
// message. In normal use, Msg is abstracted away for you, but it can be of
// interest.
type Msg map[string]interface{}

var _ fmt.Stringer = Msg{}

func (m Msg) String() string {
	return fmt.Sprintf("%#v", m)
}

func (m Msg) T() (t string) {
	tif, ok := m["t"]
	if !ok {
		return
	}
	t, _ = tif.(string)
	return
}

func (m Msg) ID() string {
	defer func() {
		recover()
	}()
	return m[m["y"].(string)].(map[string]interface{})["id"].(string)
}

// Suggested nodes in a response.
func (m Msg) Nodes() (nodes []NodeInfo) {
	b := func() string {
		defer func() {
			recover()
		}()
		return m["r"].(map[string]interface{})["nodes"].(string)
	}()
	if len(b)%26 != 0 {
		return
	}
	for i := 0; i < len(b); i += 26 {
		var n NodeInfo
		err := n.UnmarshalCompact([]byte(b[i : i+26]))
		if err != nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return
}

type KRPCError struct {
	Code int
	Msg  string
}

func (me KRPCError) Error() string {
	return fmt.Sprintf("KRPC error %d: %s", me.Code, me.Msg)
}

var _ error = KRPCError{}

func (m Msg) Error() (ret *KRPCError) {
	if m["y"] != "e" {
		return
	}
	ret = &KRPCError{}
	switch e := m["e"].(type) {
	case []interface{}:
		ret.Code = int(e[0].(int64))
		ret.Msg = e[1].(string)
	case string:
		ret.Msg = e
	default:
		logonce.Stderr.Printf(`KRPC error "e" value has unexpected type: %T`, e)
	}
	return
}

// Returns the token given in response to a get_peers request for future
// announce_peer requests to that node.
func (m Msg) AnnounceToken() (token string, ok bool) {
	defer func() { recover() }()
	token, ok = m["r"].(map[string]interface{})["token"].(string)
	return
}

type Transaction struct {
	mu             sync.Mutex
	remoteAddr     dHTAddr
	t              string
	response       chan Msg
	onResponse     func(Msg) // Called with the server locked.
	done           chan struct{}
	queryPacket    []byte
	timer          *time.Timer
	s              *Server
	retries        int
	lastSend       time.Time
	userOnResponse func(Msg)
}

// Set a function to be called with the response.
func (t *Transaction) SetResponseHandler(f func(Msg)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.userOnResponse = f
	t.tryHandleResponse()
}

func (t *Transaction) tryHandleResponse() {
	if t.userOnResponse == nil {
		return
	}
	select {
	case r := <-t.response:
		t.userOnResponse(r)
		// Shouldn't be called more than once.
		t.userOnResponse = nil
	default:
	}
}

func (t *Transaction) key() transactionKey {
	return transactionKey{
		t.remoteAddr.String(),
		t.t,
	}
}

func jitterDuration(average time.Duration, plusMinus time.Duration) time.Duration {
	return average - plusMinus/2 + time.Duration(rand.Int63n(int64(plusMinus)))
}

func (t *Transaction) startTimer() {
	t.timer = time.AfterFunc(jitterDuration(queryResendEvery, time.Second), t.timerCallback)
}

func (t *Transaction) timerCallback() {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		return
	default:
	}
	if t.retries == 2 {
		t.timeout()
		return
	}
	t.retries++
	t.sendQuery()
	if t.timer.Reset(jitterDuration(queryResendEvery, time.Second)) {
		panic("timer should have fired to get here")
	}
}

func (t *Transaction) sendQuery() error {
	err := t.s.writeToNode(t.queryPacket, t.remoteAddr)
	if err != nil {
		return err
	}
	t.lastSend = time.Now()
	return nil
}

func (t *Transaction) timeout() {
	go func() {
		t.s.mu.Lock()
		defer t.s.mu.Unlock()
		t.s.nodeTimedOut(t.remoteAddr)
	}()
	t.close()
}

func (t *Transaction) close() {
	if t.closing() {
		return
	}
	t.queryPacket = nil
	close(t.response)
	t.tryHandleResponse()
	close(t.done)
	t.timer.Stop()
	go func() {
		t.s.mu.Lock()
		defer t.s.mu.Unlock()
		t.s.deleteTransaction(t)
	}()
}

func (t *Transaction) closing() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// Abandon the transaction.
func (t *Transaction) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.close()
}

func (t *Transaction) handleResponse(m Msg) {
	t.mu.Lock()
	if t.closing() {
		t.mu.Unlock()
		return
	}
	close(t.done)
	t.mu.Unlock()
	if t.onResponse != nil {
		t.s.mu.Lock()
		t.onResponse(m)
		t.s.mu.Unlock()
	}
	t.queryPacket = nil
	select {
	case t.response <- m:
	default:
		panic("blocked handling response")
	}
	close(t.response)
	t.tryHandleResponse()
}

func (s *Server) setDefaults() (err error) {
	if s.id == "" {
		var id [20]byte
		h := crypto.SHA1.New()
		ss, err := os.Hostname()
		if err != nil {
			log.Print(err)
		}
		ss += s.socket.LocalAddr().String()
		h.Write([]byte(ss))
		if b := h.Sum(id[:0:20]); len(b) != 20 {
			panic(len(b))
		}
		if len(id) != 20 {
			panic(len(id))
		}
		s.id = string(id[:])
	}
	s.nodes = make(map[string]*node, 10000)
	return
}

// Packets to and from any address matching a range in the list are dropped.
func (s *Server) SetIPBlockList(list *iplist.IPList) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ipBlockList = list
}

func (s *Server) init() (err error) {
	err = s.setDefaults()
	if err != nil {
		return
	}
	s.closed = make(chan struct{})
	s.transactions = make(map[transactionKey]*Transaction)
	return
}

func (s *Server) processPacket(b []byte, addr dHTAddr) {
	var d Msg
	err := bencode.Unmarshal(b, &d)
	if err != nil {
		func() {
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
			log.Printf("%s: received bad krpc message from %s: %s: %q", s, addr, err, b)
		}()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d["y"] == "q" {
		s.handleQuery(addr, d)
		return
	}
	t := s.findResponseTransaction(d.T(), addr)
	if t == nil {
		//log.Printf("unexpected message: %#v", d)
		return
	}
	node := s.getNode(addr)
	node.lastGotResponse = time.Now()
	// TODO: Update node ID as this is an authoritative packet.
	go t.handleResponse(d)
	s.deleteTransaction(t)
}

func (s *Server) Serve() error {
	errChan := make(chan error)
	var err error
	go func() {
		errChan <- s.serve()
	}()

	go func() {
		err := s.bootstrap()
		if err != nil {
			log.Printf("error bootstrapping DHT: %s", err)
		}
	}()

	select {
	case <-s.closed:
		log.Printf("Shutting down the server")
		return nil
	case <-errChan:
		log.Printf("DHT server error: %s", err)
		return err
	}
}

func (s *Server) serve() error {
	var b [0x10000]byte
	for {
		n, addr, err := s.socket.ReadFrom(b[:])
		if err != nil {
			log.Print(err)
			return err
		}
		if n == len(b) {
			logonce.Stderr.Printf("received dht packet exceeds buffer size")
			continue
		}
		if s.ipBlocked(util.AddrIP(addr)) {
			continue
		}
		s.processPacket(b[:n], newDHTAddr(addr))
	}
}

func (s *Server) ipBlocked(ip net.IP) bool {
	if s.ipBlockList == nil {
		return false
	}
	return s.ipBlockList.Lookup(ip) != nil
}

// Adds directly to the node table.
func (s *Server) AddNode(ni NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes == nil {
		s.nodes = make(map[string]*node)
	}
	n := s.getNode(ni.Addr)
	if n.IDNotSet() {
		n.SetIDFromBytes(ni.ID[:])
	}
}

func (s *Server) nodeByID(id string) *node {
	for _, node := range s.nodes {
		if node.idString() == id {
			return node
		}
	}
	return nil
}

func (s *Server) handleQuery(source dHTAddr, m Msg) {
	args := m["a"].(map[string]interface{})
	node := s.getNode(source)
	node.SetIDFromString(args["id"].(string))
	node.lastGotQuery = time.Now()
	// Don't respond.
	if s.passive {
		return
	}
	switch m["q"] {
	case "ping":
		s.reply(source, m["t"].(string), nil)
	case "get_peers": // TODO: Extract common behaviour with find_node.
		targetID := args["info_hash"].(string)
		if len(targetID) != 20 {
			break
		}
		var rNodes []NodeInfo
		// TODO: Reply with "values" list if we have peers instead.
		for _, node := range s.closestGoodNodes(8, targetID) {
			rNodes = append(rNodes, node.NodeInfo())
		}
		nodesBytes := make([]byte, CompactNodeInfoLen*len(rNodes))
		for i, ni := range rNodes {
			err := ni.PutCompact(nodesBytes[i*CompactNodeInfoLen : (i+1)*CompactNodeInfoLen])
			if err != nil {
				panic(err)
			}
		}
		s.reply(source, m["t"].(string), map[string]interface{}{
			"nodes": string(nodesBytes),
			"token": "hi",
		})
	case "find_node": // TODO: Extract common behaviour with get_peers.
		targetID := args["target"].(string)
		if len(targetID) != 20 {
			log.Printf("bad DHT query: %v", m)
			return
		}
		var rNodes []NodeInfo
		if node := s.nodeByID(targetID); node != nil {
			rNodes = append(rNodes, node.NodeInfo())
		} else {
			for _, node := range s.closestGoodNodes(8, targetID) {
				rNodes = append(rNodes, node.NodeInfo())
			}
		}
		nodesBytes := make([]byte, CompactNodeInfoLen*len(rNodes))
		for i, ni := range rNodes {
			// TODO: Put IPv6 nodes into the correct dict element.
			if ni.Addr.UDPAddr().IP.To4() == nil {
				continue
			}
			err := ni.PutCompact(nodesBytes[i*CompactNodeInfoLen : (i+1)*CompactNodeInfoLen])
			if err != nil {
				log.Printf("error compacting %#v: %s", ni, err)
				continue
			}
		}
		s.reply(source, m["t"].(string), map[string]interface{}{
			"nodes": string(nodesBytes),
		})
	case "announce_peer":
		// TODO(anacrolix): Implement this lolz.
		// log.Print(m)
	case "vote":
		// TODO(anacrolix): Or reject, I don't think I want this.
	default:
		log.Printf("%s: not handling received query: q=%s", s, m["q"])
		return
	}
}

func (s *Server) reply(addr dHTAddr, t string, r map[string]interface{}) {
	if r == nil {
		r = make(map[string]interface{}, 1)
	}
	r["id"] = s.ID()
	m := map[string]interface{}{
		"t": t,
		"y": "r",
		"r": r,
	}
	b, err := bencode.Marshal(m)
	if err != nil {
		panic(err)
	}
	err = s.writeToNode(b, addr)
	if err != nil {
		log.Printf("error replying to %s: %s", addr, err)
	}
}

func (s *Server) getNode(addr dHTAddr) (n *node) {
	addrStr := addr.String()
	n = s.nodes[addrStr]
	if n == nil {
		n = &node{
			addr: addr,
		}
		if len(s.nodes) < maxNodes {
			s.nodes[addrStr] = n
		}
	}
	return
}
func (s *Server) nodeTimedOut(addr dHTAddr) {
	node, ok := s.nodes[addr.String()]
	if !ok {
		return
	}
	if node.DefinitelyGood() {
		return
	}
	if len(s.nodes) < maxNodes {
		return
	}
	delete(s.nodes, addr.String())
}

func (s *Server) writeToNode(b []byte, node dHTAddr) (err error) {
	if list := s.ipBlockList; list != nil {
		if r := list.Lookup(util.AddrIP(node.UDPAddr())); r != nil {
			err = fmt.Errorf("write to %s blocked: %s", node, r.Description)
			return
		}
	}
	n, err := s.socket.WriteTo(b, node.UDPAddr())
	if err != nil {
		err = fmt.Errorf("error writing %d bytes to %s: %#v", len(b), node, err)
		return
	}
	if n != len(b) {
		err = io.ErrShortWrite
		return
	}
	return
}

func (s *Server) findResponseTransaction(transactionID string, sourceNode dHTAddr) *Transaction {
	return s.transactions[transactionKey{
		sourceNode.String(),
		transactionID}]
}

func (s *Server) nextTransactionID() string {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], s.transactionIDInt)
	s.transactionIDInt++
	return string(b[:n])
}

func (s *Server) deleteTransaction(t *Transaction) {
	delete(s.transactions, t.key())
}

func (s *Server) addTransaction(t *Transaction) {
	if _, ok := s.transactions[t.key()]; ok {
		panic("transaction not unique")
	}
	s.transactions[t.key()] = t
}

// Returns the 20-byte server ID. This is the ID used to communicate with the
// DHT network.
func (s *Server) ID() string {
	if len(s.id) != 20 {
		panic("bad node id")
	}
	return s.id
}

func (s *Server) query(node dHTAddr, q string, a map[string]interface{}, onResponse func(Msg)) (t *Transaction, err error) {
	tid := s.nextTransactionID()
	if a == nil {
		a = make(map[string]interface{}, 1)
	}
	a["id"] = s.ID()
	d := map[string]interface{}{
		"t": tid,
		"y": "q",
		"q": q,
		"a": a,
	}
	b, err := bencode.Marshal(d)
	if err != nil {
		return
	}
	t = &Transaction{
		remoteAddr:  node,
		t:           tid,
		response:    make(chan Msg, 1),
		done:        make(chan struct{}),
		queryPacket: b,
		s:           s,
		onResponse:  onResponse,
	}
	err = t.sendQuery()
	if err != nil {
		return
	}
	s.getNode(node).lastSentQuery = time.Now()
	t.startTimer()
	s.addTransaction(t)
	return
}

// The size in bytes of a NodeInfo in its compact binary representation.
const CompactNodeInfoLen = 26

type NodeInfo struct {
	ID   [20]byte
	Addr dHTAddr
}

// Writes the node info to its compact binary representation in b. See
// CompactNodeInfoLen.
func (ni *NodeInfo) PutCompact(b []byte) error {
	if n := copy(b[:], ni.ID[:]); n != 20 {
		panic(n)
	}
	ip := util.AddrIP(ni.Addr).To4()
	if len(ip) != 4 {
		return errors.New("expected ipv4 address")
	}
	if n := copy(b[20:], ip); n != 4 {
		panic(n)
	}
	binary.BigEndian.PutUint16(b[24:], uint16(util.AddrPort(ni.Addr)))
	return nil
}

func (cni *NodeInfo) UnmarshalCompact(b []byte) error {
	if len(b) != 26 {
		return errors.New("expected 26 bytes")
	}
	util.CopyExact(cni.ID[:], b[:20])
	cni.Addr = newDHTAddr(&net.UDPAddr{
		IP:   net.IPv4(b[20], b[21], b[22], b[23]),
		Port: int(binary.BigEndian.Uint16(b[24:26])),
	})
	return nil
}

// Sends a ping query to the address given.
func (s *Server) Ping(node *net.UDPAddr) (*Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.query(newDHTAddr(node), "ping", nil, nil)
}

func (s *Server) announcePeer(node dHTAddr, infoHash string, port int, token string, impliedPort bool) (err error) {
	if port == 0 && !impliedPort {
		return errors.New("nothing to announce")
	}
	_, err = s.query(node, "announce_peer", map[string]interface{}{
		"implied_port": func() int {
			if impliedPort {
				return 1
			} else {
				return 0
			}
		}(),
		"info_hash": infoHash,
		"port":      port,
		"token":     token,
	}, func(m Msg) {
		if err := m.Error(); err != nil {
			logonce.Stderr.Printf("announce_peer response: %s", err)
			return
		}
		s.numConfirmedAnnounces++
	})
	return
}

// Add response nodes to node table.
func (s *Server) liftNodes(d Msg) {
	if d["y"] != "r" {
		return
	}
	for _, cni := range d.Nodes() {
		if util.AddrPort(cni.Addr) == 0 {
			// TODO: Why would people even do this?
			continue
		}
		if s.ipBlocked(util.AddrIP(cni.Addr)) {
			continue
		}
		n := s.getNode(cni.Addr)
		n.SetIDFromBytes(cni.ID[:])
	}
}

// Sends a find_node query to addr. targetID is the node we're looking for.
func (s *Server) findNode(addr dHTAddr, targetID string) (t *Transaction, err error) {
	t, err = s.query(addr, "find_node", map[string]interface{}{"target": targetID}, func(d Msg) {
		// Scrape peers from the response to put in the server's table before
		// handing the response back to the caller.
		s.liftNodes(d)
	})
	if err != nil {
		return
	}
	return
}

// In a get_peers response, the addresses of torrent clients involved with the
// queried info-hash.
func (m Msg) Values() (vs []util.CompactPeer) {
	r, ok := m["r"]
	if !ok {
		return
	}
	rd, ok := r.(map[string]interface{})
	if !ok {
		return
	}
	v, ok := rd["values"]
	if !ok {
		return
	}
	vl, ok := v.([]interface{})
	if !ok {
		log.Printf("unexpected krpc values type: %T", v)
		return
	}
	vs = make([]util.CompactPeer, 0, len(vl))
	for _, i := range vl {
		s, ok := i.(string)
		if !ok {
			panic(i)
		}
		var cp util.CompactPeer
		err := cp.UnmarshalBinary([]byte(s))
		if err != nil {
			log.Printf("error decoding values list element: %s", err)
			continue
		}
		vs = append(vs, cp)
	}
	return
}

func (s *Server) getPeers(addr dHTAddr, infoHash string) (t *Transaction, err error) {
	if len(infoHash) != 20 {
		err = fmt.Errorf("infohash has bad length")
		return
	}
	t, err = s.query(addr, "get_peers", map[string]interface{}{"info_hash": infoHash}, func(m Msg) {
		s.liftNodes(m)
		at, ok := m.AnnounceToken()
		if ok {
			s.getNode(addr).announceToken = at
		}
	})
	return
}

func bootstrapAddrs(nodeAddrs []string) (addrs []*net.UDPAddr, err error) {
	bootstrapNodes := nodeAddrs
	if len(bootstrapNodes) == 0 {
		bootstrapNodes = []string{
			"router.utorrent.com:6881",
			"router.bittorrent.com:6881",
		}
	}
	for _, addrStr := range bootstrapNodes {
		if addrStr != "" {
			udpAddr, err := net.ResolveUDPAddr("udp4", addrStr)
			if err != nil {
				continue
			}
			addrs = append(addrs, udpAddr)
		}
	}
	if len(addrs) == 0 {
		err = errors.New("nothing resolved")
	}
	return
}

func (s *Server) addRootNodes() error {
	addrs, err := bootstrapAddrs(s.bootstrapNodes)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		s.nodes[addr.String()] = &node{
			addr: newDHTAddr(addr),
		}
	}
	return nil
}

// Populates the node table.
func (s *Server) bootstrap() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.nodes) == 0 {
		err = s.addRootNodes()
	}
	if err != nil {
		return
	}
	for {
		var outstanding sync.WaitGroup
		for _, node := range s.nodes {
			var t *Transaction
			t, err = s.findNode(node.addr, s.id)
			if err != nil {
				err = fmt.Errorf("error sending find_node: %s", err)
				return
			}
			outstanding.Add(1)
			t.SetResponseHandler(func(Msg) {
				outstanding.Done()
			})
		}
		noOutstanding := make(chan struct{})
		go func() {
			outstanding.Wait()
			close(noOutstanding)
		}()
		s.mu.Unlock()
		select {
		case <-s.closed:
			s.mu.Lock()
			return
		case <-time.After(15 * time.Second):
		case <-noOutstanding:
		}
		s.mu.Lock()
		// log.Printf("now have %d nodes", len(s.nodes))
		if s.numGoodNodes() >= 160 {
			break
		}
	}
	return
}

func (s *Server) numGoodNodes() (num int) {
	for _, n := range s.nodes {
		if n.DefinitelyGood() {
			num++
		}
	}
	return
}

// Returns how many nodes are in the node table.
func (s *Server) NumNodes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.nodes)
}

// Exports the current node table.
func (s *Server) Nodes() (nis []NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, node := range s.nodes {
		// if !node.Good() {
		// 	continue
		// }
		ni := NodeInfo{
			Addr: node.addr,
		}
		if n := copy(ni.ID[:], node.idString()); n != 20 && n != 0 {
			panic(n)
		}
		nis = append(nis, ni)
	}
	return
}

// Stops the server network activity. This is all that's required to clean-up a Server.
func (s *Server) Close() {
	s.mu.Lock()
	select {
	case <-s.closed:
	default:
		close(s.closed)
		s.socket.Close()
	}
	s.mu.Unlock()
}

var maxDistance big.Int

func init() {
	var zero big.Int
	maxDistance.SetBit(&zero, 160, 1)
}

func (s *Server) closestGoodNodes(k int, targetID string) []*node {
	return s.closestNodes(k, nodeIDFromString(targetID), func(n *node) bool { return n.DefinitelyGood() })
}

func (s *Server) closestNodes(k int, target nodeID, filter func(*node) bool) []*node {
	sel := newKClosestNodesSelector(k, target)
	idNodes := make(map[string]*node, len(s.nodes))
	for _, node := range s.nodes {
		if !filter(node) {
			continue
		}
		sel.Push(node.id)
		idNodes[node.idString()] = node
	}
	ids := sel.IDs()
	ret := make([]*node, 0, len(ids))
	for _, id := range ids {
		ret = append(ret, idNodes[id.String()])
	}
	return ret
}
