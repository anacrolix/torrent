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

	"bitbucket.org/anacrolix/sync"

	"bitbucket.org/anacrolix/go.torrent/iplist"

	"bitbucket.org/anacrolix/go.torrent/logonce"
	"bitbucket.org/anacrolix/go.torrent/util"
	"github.com/anacrolix/libtorgo/bencode"
)

const maxNodes = 10000

// Uniquely identifies a transaction to us.
type transactionKey struct {
	RemoteAddr string // host:port
	T          string // The KRPC transaction ID.
}

type Server struct {
	id               string
	socket           net.PacketConn
	transactions     map[transactionKey]*transaction
	transactionIDInt uint64
	nodes            map[string]*Node // Keyed by dHTAddr.String().
	mu               sync.Mutex
	closed           chan struct{}
	passive          bool // Don't respond to queries.
	ipBlockList      *iplist.IPList

	NumConfirmedAnnounces int
}

type dHTAddr interface {
	net.Addr
}

func newDHTAddr(addr *net.UDPAddr) (ret dHTAddr) {
	ret = addr
	return
}

type ServerConfig struct {
	Addr    string
	Conn    net.PacketConn
	Passive bool // Don't respond to queries.
}

type serverStats struct {
	NumGoodNodes               int
	NumNodes                   int
	NumOutstandingTransactions int
}

func (s *Server) Stats() (ss serverStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nodes {
		if n.DefinitelyGood() {
			ss.NumGoodNodes++
		}
	}
	ss.NumNodes = len(s.nodes)
	ss.NumOutstandingTransactions = len(s.transactions)
	return
}

func (s *Server) LocalAddr() net.Addr {
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
	go func() {
		err := s.bootstrap()
		if err != nil {
			log.Printf("error bootstrapping DHT: %s", err)
		}
	}()
	return
}

func (s *Server) String() string {
	return fmt.Sprintf("dht server on %s", s.socket.LocalAddr())
}

type Node struct {
	addr          dHTAddr
	id            string
	announceToken string

	lastGotQuery    time.Time
	lastGotResponse time.Time
	lastSentQuery   time.Time
}

func (n *Node) NodeInfo() (ret NodeInfo) {
	ret.Addr = n.addr
	if n := copy(ret.ID[:], n.id); n != 20 {
		panic(n)
	}
	return
}

func (n *Node) DefinitelyGood() bool {
	if len(n.id) != 20 {
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

func (m Msg) Nodes() []NodeInfo {
	var r findNodeResponse
	if err := r.UnmarshalKRPCMsg(m); err != nil {
		return nil
	}
	return r.Nodes
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
func (m Msg) AnnounceToken() string {
	defer func() { recover() }()
	return m["r"].(map[string]interface{})["token"].(string)
}

type transaction struct {
	mu          sync.Mutex
	remoteAddr  dHTAddr
	t           string
	Response    chan Msg
	onResponse  func(Msg) // Called with the server locked.
	done        chan struct{}
	queryPacket []byte
	timer       *time.Timer
	s           *Server
	retries     int
}

func (t *transaction) Key() transactionKey {
	return transactionKey{
		t.remoteAddr.String(),
		t.t,
	}
}

func jitterDuration(average time.Duration, plusMinus time.Duration) time.Duration {
	return average - plusMinus/2 + time.Duration(rand.Int63n(int64(plusMinus)))
}

func (t *transaction) startTimer() {
	t.timer = time.AfterFunc(jitterDuration(20*time.Second, time.Second), t.timerCallback)
}

func (t *transaction) timerCallback() {
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
	if t.timer.Reset(jitterDuration(20*time.Second, time.Second)) {
		panic("timer should have fired to get here")
	}
}

func (t *transaction) sendQuery() error {
	return t.s.writeToNode(t.queryPacket, t.remoteAddr)
}

func (t *transaction) timeout() {
	go func() {
		t.s.mu.Lock()
		defer t.s.mu.Unlock()
		t.s.nodeTimedOut(t.remoteAddr)
	}()
	t.close()
}

func (t *transaction) close() {
	if t.closing() {
		return
	}
	t.queryPacket = nil
	close(t.Response)
	close(t.done)
	t.timer.Stop()
	go func() {
		t.s.mu.Lock()
		defer t.s.mu.Unlock()
		t.s.deleteTransaction(t)
	}()
}

func (t *transaction) closing() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (t *transaction) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.close()
}

func (t *transaction) handleResponse(m Msg) {
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
	case t.Response <- m:
	default:
		panic("blocked handling response")
	}
	close(t.Response)
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
	s.nodes = make(map[string]*Node, 10000)
	return
}

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
	s.transactions = make(map[transactionKey]*transaction)
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

func (s *Server) serve() error {
	var b [0x10000]byte
	for {
		n, addr, err := s.socket.ReadFrom(b[:])
		if err != nil {
			return err
		}
		if n == len(b) {
			logonce.Stderr.Printf("received dht packet exceeds buffer size")
			continue
		}
		s.processPacket(b[:n], newDHTAddr(addr.(*net.UDPAddr)))
	}
}

func (s *Server) ipBlocked(ip net.IP) bool {
	if s.ipBlockList == nil {
		return false
	}
	return s.ipBlockList.Lookup(ip) != nil
}

func (s *Server) AddNode(ni NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodes == nil {
		s.nodes = make(map[string]*Node)
	}
	n := s.getNode(ni.Addr)
	if n.id == "" {
		n.id = string(ni.ID[:])
	}
}

func (s *Server) nodeByID(id string) *Node {
	for _, node := range s.nodes {
		if node.id == id {
			return node
		}
	}
	return nil
}

func (s *Server) handleQuery(source dHTAddr, m Msg) {
	args := m["a"].(map[string]interface{})
	node := s.getNode(source)
	node.id = args["id"].(string)
	node.lastGotQuery = time.Now()
	// Don't respond.
	if s.passive {
		return
	}
	switch m["q"] {
	case "ping":
		s.reply(source, m["t"].(string), nil)
	case "get_peers":
		targetID := args["info_hash"].(string)
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
	case "find_node":
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
			err := ni.PutCompact(nodesBytes[i*CompactNodeInfoLen : (i+1)*CompactNodeInfoLen])
			if err != nil {
				panic(err)
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
	r["id"] = s.IDString()
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

func (s *Server) getNode(addr dHTAddr) (n *Node) {
	n = s.nodes[addr.String()]
	if n == nil {
		n = &Node{
			addr: addr,
		}
		if len(s.nodes) < maxNodes {
			s.nodes[addr.String()] = n
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
		if r := list.Lookup(util.AddrIP(node)); r != nil {
			err = fmt.Errorf("write to %s blocked: %s", node, r.Description)
			return
		}
	}
	n, err := s.socket.WriteTo(b, node)
	if err != nil {
		err = fmt.Errorf("error writing %d bytes to %s: %s", len(b), node, err)
		return
	}
	if n != len(b) {
		err = io.ErrShortWrite
		return
	}
	return
}

func (s *Server) findResponseTransaction(transactionID string, sourceNode dHTAddr) *transaction {
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

func (s *Server) deleteTransaction(t *transaction) {
	delete(s.transactions, t.Key())
}

func (s *Server) addTransaction(t *transaction) {
	if _, ok := s.transactions[t.Key()]; ok {
		panic("transaction not unique")
	}
	s.transactions[t.Key()] = t
}

func (s *Server) IDString() string {
	if len(s.id) != 20 {
		panic("bad node id")
	}
	return s.id
}

func (s *Server) query(node dHTAddr, q string, a map[string]interface{}) (t *transaction, err error) {
	tid := s.nextTransactionID()
	if a == nil {
		a = make(map[string]interface{}, 1)
	}
	a["id"] = s.IDString()
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
	t = &transaction{
		remoteAddr:  node,
		t:           tid,
		Response:    make(chan Msg, 1),
		done:        make(chan struct{}),
		queryPacket: b,
		s:           s,
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

const CompactNodeInfoLen = 26

type NodeInfo struct {
	ID   [20]byte
	Addr dHTAddr
}

func (ni *NodeInfo) PutCompact(b []byte) error {
	if n := copy(b[:], ni.ID[:]); n != 20 {
		panic(n)
	}
	ip := util.AddrIP(ni.Addr).To4()
	if len(ip) != 4 {
		panic(ip)
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
	if 20 != copy(cni.ID[:], b[:20]) {
		panic("impossibru!")
	}
	cni.Addr = newDHTAddr(&net.UDPAddr{
		IP:   net.IPv4(b[20], b[21], b[22], b[23]),
		Port: int(binary.BigEndian.Uint16(b[24:26])),
	})
	return nil
}

func (s *Server) Ping(node *net.UDPAddr) (*transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.query(newDHTAddr(node), "ping", nil)
}

// Announce a local peer. This can only be done to nodes that gave us an
// announce token, which is received in responses during GetPeers. It's
// recommended then that GetPeers is called before this method.
func (s *Server) AnnouncePeer(port int, impliedPort bool, infoHash string) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, node := range s.closestNodes(160, infoHash, func(n *Node) bool {
		return n.announceToken != ""
	}) {
		err = s.announcePeer(node.addr, infoHash, port, node.announceToken, impliedPort)
		if err != nil {
			break
		}
	}
	return
}

func (s *Server) announcePeer(node dHTAddr, infoHash string, port int, token string, impliedPort bool) error {
	t, err := s.query(node, "announce_peer", map[string]interface{}{
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
	})
	t.setOnResponse(func(m Msg) {
		if err := m.Error(); err != nil {
			logonce.Stderr.Printf("announce_peer response: %s", err)
			return
		}
		s.NumConfirmedAnnounces++
	})
	return err
}

type findNodeResponse struct {
	Nodes []NodeInfo
}

func getResponseNodes(m Msg) (s string, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		err = fmt.Errorf("couldn't get response nodes: %s: %#v", r, m)
	}()
	s = m["r"].(map[string]interface{})["nodes"].(string)
	return
}

func (me *findNodeResponse) UnmarshalKRPCMsg(m Msg) error {
	b, err := getResponseNodes(m)
	if err != nil {
		return err
	}
	for i := 0; i < len(b); i += 26 {
		var n NodeInfo
		err := n.UnmarshalCompact([]byte(b[i : i+26]))
		if err != nil {
			return err
		}
		me.Nodes = append(me.Nodes, n)
	}
	return nil
}

func (t *transaction) setOnResponse(f func(m Msg)) {
	if t.onResponse != nil {
		panic(t.onResponse)
	}
	t.onResponse = f
}

func unmarshalNodeInfoBinary(b []byte) (ret []NodeInfo, err error) {
	if len(b)%26 != 0 {
		err = errors.New("bad buffer length")
		return
	}
	ret = make([]NodeInfo, 0, len(b)/26)
	for i := 0; i < len(b); i += 26 {
		var ni NodeInfo
		err = ni.UnmarshalCompact(b[i : i+26])
		if err != nil {
			return
		}
		ret = append(ret, ni)
	}
	return
}

func extractNodes(d Msg) (nodes []NodeInfo, err error) {
	if d["y"] != "r" {
		return
	}
	r, ok := d["r"]
	if !ok {
		err = errors.New("missing r dict")
		return
	}
	rd, ok := r.(map[string]interface{})
	if !ok {
		err = errors.New("bad r value type")
		return
	}
	n, ok := rd["nodes"]
	if !ok {
		return
	}
	ns, ok := n.(string)
	if !ok {
		err = errors.New("bad nodes value type")
		return
	}
	return unmarshalNodeInfoBinary([]byte(ns))
}

func (s *Server) liftNodes(d Msg) {
	if d["y"] != "r" {
		return
	}
	var r findNodeResponse
	err := r.UnmarshalKRPCMsg(d)
	if err != nil {
		// log.Print(err)
	} else {
		for _, cni := range r.Nodes {
			if util.AddrPort(cni.Addr) == 0 {
				// TODO: Why would people even do this?
				continue
			}
			if s.ipBlocked(util.AddrIP(cni.Addr)) {
				continue
			}
			n := s.getNode(cni.Addr)
			n.id = string(cni.ID[:])
		}
		// log.Printf("lifted %d nodes", len(r.Nodes))
	}
}

// Sends a find_node query to addr. targetID is the node we're looking for.
func (s *Server) findNode(addr dHTAddr, targetID string) (t *transaction, err error) {
	t, err = s.query(addr, "find_node", map[string]interface{}{"target": targetID})
	if err != nil {
		return
	}
	// Scrape peers from the response to put in the server's table before
	// handing the response back to the caller.
	t.setOnResponse(func(d Msg) {
		s.liftNodes(d)
	})
	return
}

func extractValues(m Msg) (vs []util.CompactPeer) {
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

func (s *Server) getPeers(addr dHTAddr, infoHash string) (t *transaction, err error) {
	if len(infoHash) != 20 {
		err = fmt.Errorf("infohash has bad length")
		return
	}
	t, err = s.query(addr, "get_peers", map[string]interface{}{"info_hash": infoHash})
	if err != nil {
		return
	}
	t.setOnResponse(func(m Msg) {
		s.liftNodes(m)
		s.getNode(addr).announceToken = m.AnnounceToken()
	})
	return
}

func bootstrapAddr() (*net.UDPAddr, error) {
	return net.ResolveUDPAddr("udp4", "router.utorrent.com:6881")
}

func (s *Server) addRootNode() error {
	addr, err := bootstrapAddr()
	if err != nil {
		return err
	}
	s.nodes[addr.String()] = &Node{
		addr: newDHTAddr(addr),
	}
	return nil
}

// Populates the node table.
func (s *Server) bootstrap() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.nodes) == 0 {
		err = s.addRootNode()
	}
	if err != nil {
		return
	}
	for {
		var outstanding sync.WaitGroup
		for _, node := range s.nodes {
			var t *transaction
			t, err = s.findNode(node.addr, s.id)
			if err != nil {
				log.Printf("error sending find_node: %s", err)
				continue
			}
			outstanding.Add(1)
			go func() {
				<-t.Response
				outstanding.Done()
			}()
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

func (s *Server) NumNodes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.nodes)
}

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
		if n := copy(ni.ID[:], node.id); n != 20 && n != 0 {
			panic(n)
		}
		nis = append(nis, ni)
	}
	return
}

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

type distance interface {
	Cmp(distance) int
	BitCount() int
	IsZero() bool
}

type bigIntDistance struct {
	big.Int
}

// How many bits?
func bitCount(n *big.Int) int {
	var count int = 0
	for _, b := range n.Bytes() {
		count += int(bitCounts[b])
	}
	return count
}

// The bit counts for each byte value (0 - 255).
var bitCounts = []int8{
	// Generated by Java BitCount of all values from 0 to 255
	0, 1, 1, 2, 1, 2, 2, 3,
	1, 2, 2, 3, 2, 3, 3, 4,
	1, 2, 2, 3, 2, 3, 3, 4,
	2, 3, 3, 4, 3, 4, 4, 5,
	1, 2, 2, 3, 2, 3, 3, 4,
	2, 3, 3, 4, 3, 4, 4, 5,
	2, 3, 3, 4, 3, 4, 4, 5,
	3, 4, 4, 5, 4, 5, 5, 6,
	1, 2, 2, 3, 2, 3, 3, 4,
	2, 3, 3, 4, 3, 4, 4, 5,
	2, 3, 3, 4, 3, 4, 4, 5,
	3, 4, 4, 5, 4, 5, 5, 6,
	2, 3, 3, 4, 3, 4, 4, 5,
	3, 4, 4, 5, 4, 5, 5, 6,
	3, 4, 4, 5, 4, 5, 5, 6,
	4, 5, 5, 6, 5, 6, 6, 7,
	1, 2, 2, 3, 2, 3, 3, 4,
	2, 3, 3, 4, 3, 4, 4, 5,
	2, 3, 3, 4, 3, 4, 4, 5,
	3, 4, 4, 5, 4, 5, 5, 6,
	2, 3, 3, 4, 3, 4, 4, 5,
	3, 4, 4, 5, 4, 5, 5, 6,
	3, 4, 4, 5, 4, 5, 5, 6,
	4, 5, 5, 6, 5, 6, 6, 7,
	2, 3, 3, 4, 3, 4, 4, 5,
	3, 4, 4, 5, 4, 5, 5, 6,
	3, 4, 4, 5, 4, 5, 5, 6,
	4, 5, 5, 6, 5, 6, 6, 7,
	3, 4, 4, 5, 4, 5, 5, 6,
	4, 5, 5, 6, 5, 6, 6, 7,
	4, 5, 5, 6, 5, 6, 6, 7,
	5, 6, 6, 7, 6, 7, 7, 8,
}

func (me bigIntDistance) BitCount() int {
	return bitCount(&me.Int)
}

func (me bigIntDistance) Cmp(d bigIntDistance) int {
	return me.Int.Cmp(&d.Int)
}

func (me bigIntDistance) IsZero() bool {
	var zero big.Int
	return me.Int.Cmp(&zero) == 0
}

type bitCountDistance int

func (me bitCountDistance) BitCount() int { return int(me) }

func (me bitCountDistance) Cmp(rhs distance) int {
	rhs_ := rhs.(bitCountDistance)
	if me < rhs_ {
		return -1
	} else if me == rhs_ {
		return 0
	} else {
		return 1
	}
}

func (me bitCountDistance) IsZero() bool {
	return me == 0
}

// Below are 2 versions of idDistance. Only one can be active.

func idDistance(a, b string) (ret bigIntDistance) {
	if len(a) != 20 {
		panic(a)
	}
	if len(b) != 20 {
		panic(b)
	}
	var x, y big.Int
	x.SetBytes([]byte(a))
	y.SetBytes([]byte(b))
	ret.Int.Xor(&x, &y)
	return ret
}

// func idDistance(a, b string) bitCountDistance {
// 	ret := 0
// 	for i := 0; i < 20; i++ {
// 		for j := uint(0); j < 8; j++ {
// 			ret += int(a[i]>>j&1 ^ b[i]>>j&1)
// 		}
// 	}
// 	return bitCountDistance(ret)
// }

func (s *Server) closestGoodNodes(k int, targetID string) []*Node {
	return s.closestNodes(k, targetID, func(n *Node) bool { return n.DefinitelyGood() })
}

func (s *Server) closestNodes(k int, targetID string, filter func(*Node) bool) []*Node {
	sel := newKClosestNodesSelector(k, targetID)
	idNodes := make(map[string]*Node, len(s.nodes))
	for _, node := range s.nodes {
		if !filter(node) {
			continue
		}
		sel.Push(node.id)
		idNodes[node.id] = node
	}
	ids := sel.IDs()
	ret := make([]*Node, 0, len(ids))
	for _, id := range ids {
		ret = append(ret, idNodes[id])
	}
	return ret
}
