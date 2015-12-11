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
	"hash/crc32"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/sync"
	"github.com/tylertreat/BoomFilters"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/logonce"
)

const (
	maxNodes = 320
)

var (
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
	nodes            map[string]*Node // Keyed by dHTAddr.String().
	mu               sync.Mutex
	closed           chan struct{}
	ipBlockList      iplist.Ranger
	badNodes         *boom.BloomFilter
	hooks            map[string]KRPCHook

	numConfirmedAnnounces int
	bootstrapNodes        []string
	config                ServerConfig
}

// Returned when a caller sets ID manually but fails to set NoSecurity
// configuration flag. Better to be explicit and make outcomes
// predictable than implicitly disable one option when provided another.
var ErrConflictingConfigNoSecNodeId = errors.New("Cannot manually set NodeId without also setting NoSecurity.")

type ServerConfig struct {
	Addr string // Listen address. Used if Conn is nil.
	// To set NodeId manually. Caller MUST also set NoSecurity,
	// and NodeId in this case is 20 bytes cast to string, not
	// a hex or baseXX encoded ID.
	NodeId string
	Conn   net.PacketConn
	// Don't respond to queries from other nodes.
	Passive bool
	// DHT Bootstrap nodes
	BootstrapNodes []string
	// Disable bootstrapping from global servers even if given no BootstrapNodes.
	// This creates a solitary node that awaits other nodes; it's only useful if
	// you're creating your own DHT and want to avoid accidental crossover, without
	// spoofing a bootstrap node and filling your logs with connection errors.
	NoBootstrap bool
	// Disable the DHT security extension:
	// http://www.libtorrent.org/dht_sec.html.
	NoSecurity bool
	// Initial IP blocklist to use. Applied before serving and bootstrapping
	// begins.
	IPBlocklist iplist.Ranger
	// Used to secure the server's ID. Defaults to the Conn's LocalAddr().
	PublicIP net.IP
	// Hook functions to be called when matching KRPC queries arrive.
	// Hooks run prior to default KRPC handling, and may modify messages
	// or indicate whether to skip default handling.
	// Hooks can therefore replace, extend, or disable KRPC query handling
	// in a Query-type specific way.
	KRPCHooks map[string]KRPCHook
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
	// Nodes that have been blocked.
	BadNodes uint
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
	ss.BadNodes = s.badNodes.Count()
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
	s = &Server{
		config:      *c,
		ipBlockList: c.IPBlocklist,
		badNodes:    boom.NewBloomFilter(1000, 0.1),
		hooks:       c.KRPCHooks,
	}
	if c.Conn != nil {
		s.socket = c.Conn
	} else {
		s.socket, err = makeSocket(c.Addr)
		if err != nil {
			return
		}
	}
	s.bootstrapNodes = c.BootstrapNodes
	if c.NodeId != "" {
		if !c.NoSecurity {
			return nil, ErrConflictingConfigNoSecNodeId
		}
		s.id = c.NodeId
	}
	err = s.init()
	if err != nil {
		return
	}
	go func() {
		err := s.serve()
		select {
		case <-s.closed:
			return
		default:
		}
		if err != nil {
			panic(err)
		}
	}()
	if !s.config.NoBootstrap {
		go func() {
			err := s.bootstrap()
			if err != nil {
				select {
				case <-s.closed:
				default:
					log.Printf("error bootstrapping DHT: %s", err)
				}
			}
		}()
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

func (nid *nodeID) ByteString() string {
	var buf [20]byte
	b := nid.i.Bytes()
	copy(buf[20-len(b):], b)
	return string(buf[:])
}

type Node struct {
	addr          DHTAddr
	id            nodeID
	announceToken string

	lastGotQuery    time.Time
	lastGotResponse time.Time
	lastSentQuery   time.Time
}

func (n *Node) IsSecure() bool {
	if n.id.IsUnset() {
		return false
	}
	return NodeIdSecure(n.id.ByteString(), n.addr.IP())
}

func (n *Node) idString() string {
	return n.id.ByteString()
}

func (n *Node) SetIDFromBytes(b []byte) {
	if len(b) != 20 {
		panic(b)
	}
	n.id.i.SetBytes(b)
	n.id.set = true
}

func (n *Node) SetIDFromString(s string) {
	n.SetIDFromBytes([]byte(s))
}

func (n *Node) IDNotSet() bool {
	return n.id.i.Int64() == 0
}

func (n *Node) NodeInfo() (ret NodeInfo) {
	ret.Addr = n.addr
	if n := copy(ret.ID[:], n.idString()); n != 20 {
		panic(n)
	}
	return
}

func (n *Node) DefinitelyGood() bool {
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

type Transaction struct {
	mu             sync.Mutex
	remoteAddr     DHTAddr
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

func maskForIP(ip net.IP) []byte {
	switch {
	case ip.To4() != nil:
		return []byte{0x03, 0x0f, 0x3f, 0xff}
	default:
		return []byte{0x01, 0x03, 0x07, 0x0f, 0x1f, 0x3f, 0x7f, 0xff}
	}
}

// Generate the CRC used to make or validate secure node ID.
func crcIP(ip net.IP, rand uint8) uint32 {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	// Copy IP so we can make changes. Go sux at this.
	ip = append(make(net.IP, 0, len(ip)), ip...)
	mask := maskForIP(ip)
	for i := range mask {
		ip[i] &= mask[i]
	}
	r := rand & 7
	ip[0] |= r << 5
	return crc32.Checksum(ip[:len(mask)], crc32.MakeTable(crc32.Castagnoli))
}

// Makes a node ID secure, in-place. The ID is 20 raw bytes.
// http://www.libtorrent.org/dht_sec.html
func SecureNodeId(id []byte, ip net.IP) {
	crc := crcIP(ip, id[19])
	id[0] = byte(crc >> 24 & 0xff)
	id[1] = byte(crc >> 16 & 0xff)
	id[2] = byte(crc>>8&0xf8) | id[2]&7
}

// Returns whether the node ID is considered secure. The id is the 20 raw
// bytes. http://www.libtorrent.org/dht_sec.html
func NodeIdSecure(id string, ip net.IP) bool {
	if len(id) != 20 {
		panic(fmt.Sprintf("%q", id))
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	crc := crcIP(ip, id[19])
	if id[0] != byte(crc>>24&0xff) {
		return false
	}
	if id[1] != byte(crc>>16&0xff) {
		return false
	}
	if id[2]&0xf8 != byte(crc>>8&0xf8) {
		return false
	}
	return true
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
		publicIP := func() net.IP {
			if s.config.PublicIP != nil {
				return s.config.PublicIP
			} else {
				return missinggo.AddrIP(s.socket.LocalAddr())
			}
		}()
		SecureNodeId(id[:], publicIP)
		s.id = string(id[:])
	}
	s.nodes = make(map[string]*Node, maxNodes)
	return
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

func (s *Server) init() (err error) {
	err = s.setDefaults()
	if err != nil {
		return
	}
	s.closed = make(chan struct{})
	s.transactions = make(map[transactionKey]*Transaction)
	return
}

func (s *Server) processPacket(b []byte, addr DHTAddr) {
	if len(b) < 2 || b[0] != 'd' || b[len(b)-1] != 'e' {
		// KRPC messages are bencoded dicts.
		readNotKRPCDict.Add(1)
		return
	}
	var d Msg
	err := bencode.Unmarshal(b, &d)
	if err != nil {
		readUnmarshalError.Add(1)
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
			// if missinggo.CryHeard() {
			// 	log.Printf("%s: received bad krpc message from %s: %s: %+q", s, addr, err, b)
			// }
		}()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Y == "q" {
		readQuery.Add(1)
		s.handleQuery(addr, d)
		return
	}
	t := s.findResponseTransaction(d.T, addr)
	if t == nil {
		//log.Printf("unexpected message: %#v", d)
		return
	}
	node := s.getNode(addr, d.SenderID())
	node.lastGotResponse = time.Now()
	// TODO: Update node ID as this is an authoritative packet.
	go t.handleResponse(d)
	s.deleteTransaction(t)
}

func (s *Server) serve() error {
	var b [0x10000]byte
	for {
		n, addr, err := s.socket.ReadFrom(b[:])
		if err != nil {
			return err
		}
		read.Add(1)
		if n == len(b) {
			logonce.Stderr.Printf("received dht packet exceeds buffer size")
			continue
		}
		s.mu.Lock()
		blocked := s.ipBlocked(missinggo.AddrIP(addr))
		s.mu.Unlock()
		if blocked {
			readBlocked.Add(1)
			continue
		}
		s.processPacket(b[:n], newDHTAddr(addr))
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
func (s *Server) AddNode(ni NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes == nil {
		s.nodes = make(map[string]*Node)
	}
	s.getNode(ni.Addr, string(ni.ID[:]))
}

func (s *Server) nodeByID(id string) *Node {
	for _, node := range s.nodes {
		if node.idString() == id {
			return node
		}
	}
	return nil
}

func (s *Server) handleQuery(source DHTAddr, m Msg) {
	var (
		newmsg *Msg
		skip   bool
	)
	node := s.getNode(source, m.SenderID())
	node.lastGotQuery = time.Now()
	// Don't respond.
	if s.config.Passive {
		return
	}
	// Call any hooks that may apply
	if hook, ok := s.hooks[m.Q]; ok {
		newmsg, skip = hook(&source, node, &m)
		if newmsg != nil {
			m = *newmsg
		}
	}
	// Unless told not to by hook, proceed with normal handling.
	if skip {
		return
	}
	args := m.A
	switch m.Q {
	case "ping":
		s.reply(source, m.T, Return{})
	case "get_peers": // TODO: Extract common behaviour with find_node.
		targetID := args.InfoHash
		if len(targetID) != 20 {
			break
		}
		var rNodes []NodeInfo
		// TODO: Reply with "values" list if we have peers instead.
		for _, node := range s.closestGoodNodes(8, targetID) {
			rNodes = append(rNodes, node.NodeInfo())
		}
		s.reply(source, m.T, Return{
			Nodes: rNodes,
			// TODO: Generate this dynamically, and store it for the source.
			Token: "hi",
		})
	case "find_node": // TODO: Extract common behaviour with get_peers.
		targetID := args.Target
		if len(targetID) != 20 {
			log.Printf("bad DHT query: %v", m)
			return
		}
		var rNodes []NodeInfo
		if node := s.nodeByID(targetID); node != nil {
			rNodes = append(rNodes, node.NodeInfo())
		} else {
			// This will probably cause a crash for IPv6, but meh.
			for _, node := range s.closestGoodNodes(8, targetID) {
				rNodes = append(rNodes, node.NodeInfo())
			}
		}
		s.reply(source, m.T, Return{
			Nodes: rNodes,
		})
	case "announce_peer":
		// TODO(anacrolix): Implement this lolz.
		// log.Print(m)
	case "vote":
		// TODO(anacrolix): Or reject, I don't think I want this.
	default:
		log.Printf("%s: not handling received query: q=%s", s, m.Q)
		return
	}
}

func (s *Server) reply(addr DHTAddr, t string, r Return) {
	r.ID = s.ID()
	m := Msg{
		T: t,
		Y: "r",
		R: &r,
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

// Returns a node struct for the addr. It is taken from the table or created
// and possibly added if required and meets validity constraints.
func (s *Server) getNode(addr DHTAddr, id string) (n *Node) {
	addrStr := addr.String()
	n = s.nodes[addrStr]
	if n != nil {
		if id != "" {
			n.SetIDFromString(id)
		}
		return
	}
	n = &Node{
		addr: addr,
	}
	if len(id) == 20 {
		n.SetIDFromString(id)
	}
	if len(s.nodes) >= maxNodes {
		return
	}
	if !s.config.NoSecurity && !n.IsSecure() {
		return
	}
	if s.badNodes.Test([]byte(addrStr)) {
		return
	}
	s.nodes[addrStr] = n
	return
}

func (s *Server) nodeTimedOut(addr DHTAddr) {
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

func (s *Server) writeToNode(b []byte, node DHTAddr) (err error) {
	if list := s.ipBlockList; list != nil {
		if r, ok := list.Lookup(missinggo.AddrIP(node.UDPAddr())); ok {
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

func (s *Server) findResponseTransaction(transactionID string, sourceNode DHTAddr) *Transaction {
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

func (s *Server) query(node DHTAddr, q string, a map[string]interface{}, onResponse func(Msg)) (t *Transaction, err error) {
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
	// BEP 43. Outgoing queries from uncontactiable nodes should contain
	// "ro":1 in the top level dictionary.
	if s.config.Passive {
		d["ro"] = 1
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
	s.getNode(node, "").lastSentQuery = time.Now()
	t.startTimer()
	s.addTransaction(t)
	return
}

// The size in bytes of a NodeInfo in its compact binary representation.
const CompactIPv4NodeInfoLen = 26

type NodeInfo struct {
	ID   [20]byte
	Addr DHTAddr
}

// Writes the node info to its compact binary representation in b. See
// CompactNodeInfoLen.
func (ni *NodeInfo) PutCompact(b []byte) error {
	if n := copy(b[:], ni.ID[:]); n != 20 {
		panic(n)
	}
	ip := missinggo.AddrIP(ni.Addr).To4()
	if len(ip) != 4 {
		return errors.New("expected ipv4 address")
	}
	if n := copy(b[20:], ip); n != 4 {
		panic(n)
	}
	binary.BigEndian.PutUint16(b[24:], uint16(missinggo.AddrPort(ni.Addr)))
	return nil
}

func (cni *NodeInfo) UnmarshalCompactIPv4(b []byte) error {
	if len(b) != 26 {
		return errors.New("expected 26 bytes")
	}
	missinggo.CopyExact(cni.ID[:], b[:20])
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

func (s *Server) announcePeer(node DHTAddr, infoHash string, port int, token string, impliedPort bool) (err error) {
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
			announceErrors.Add(1)
			// log.Print(token)
			// logonce.Stderr.Printf("announce_peer response: %s", err)
			return
		}
		s.numConfirmedAnnounces++
	})
	return
}

// Add response nodes to node table.
func (s *Server) liftNodes(d Msg) {
	if d.Y != "r" {
		return
	}
	for _, cni := range d.R.Nodes {
		if missinggo.AddrPort(cni.Addr) == 0 {
			// TODO: Why would people even do this?
			continue
		}
		if s.ipBlocked(missinggo.AddrIP(cni.Addr)) {
			continue
		}
		n := s.getNode(cni.Addr, string(cni.ID[:]))
		n.SetIDFromBytes(cni.ID[:])
	}
}

// Sends a find_node query to addr. targetID is the node we're looking for.
func (s *Server) findNode(addr DHTAddr, targetID string) (t *Transaction, err error) {
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

type Peer struct {
	IP   net.IP
	Port int
}

func (me *Peer) String() string {
	return net.JoinHostPort(me.IP.String(), strconv.FormatInt(int64(me.Port), 10))
}

func (s *Server) getPeers(addr DHTAddr, infoHash string) (t *Transaction, err error) {
	if len(infoHash) != 20 {
		err = fmt.Errorf("infohash has bad length")
		return
	}
	t, err = s.query(addr, "get_peers", map[string]interface{}{"info_hash": infoHash}, func(m Msg) {
		s.liftNodes(m)
		if m.R != nil && m.R.Token != "" {
			s.getNode(addr, m.SenderID()).announceToken = m.R.Token
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
		udpAddr, err := net.ResolveUDPAddr("udp4", addrStr)
		if err != nil {
			continue
		}
		addrs = append(addrs, udpAddr)
	}
	if len(addrs) == 0 {
		err = errors.New("nothing resolved")
	}
	return
}

// Adds bootstrap nodes directly to table, if there's room. Node ID security
// is bypassed, but the IP blocklist is not.
func (s *Server) addRootNodes() error {
	addrs, err := bootstrapAddrs(s.bootstrapNodes)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if len(s.nodes) >= maxNodes {
			break
		}
		if s.nodes[addr.String()] != nil {
			continue
		}
		if s.ipBlocked(addr.IP) {
			log.Printf("dht root node is in the blocklist: %s", addr.IP)
			continue
		}
		s.nodes[addr.String()] = &Node{
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

func (s *Server) closestGoodNodes(k int, targetID string) []*Node {
	return s.closestNodes(k, nodeIDFromString(targetID), func(n *Node) bool { return n.DefinitelyGood() })
}

func (s *Server) closestNodes(k int, target nodeID, filter func(*Node) bool) []*Node {
	sel := newKClosestNodesSelector(k, target)
	idNodes := make(map[string]*Node, len(s.nodes))
	for _, node := range s.nodes {
		if !filter(node) {
			continue
		}
		sel.Push(node.id)
		idNodes[node.idString()] = node
	}
	ids := sel.IDs()
	ret := make([]*Node, 0, len(ids))
	for _, id := range ids {
		ret = append(ret, idNodes[id.ByteString()])
	}
	return ret
}

func (me *Server) badNode(addr DHTAddr) {
	me.badNodes.Add([]byte(addr.String()))
	delete(me.nodes, addr.String())
}
