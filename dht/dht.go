package dht

import (
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"bitbucket.org/anacrolix/go.torrent/util"
	"crypto"
	_ "crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/nsf/libtorgo/bencode"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

type Server struct {
	ID               string
	Socket           *net.UDPConn
	transactions     []*transaction
	transactionIDInt uint64
	nodes            map[string]*Node // Keyed by *net.UDPAddr.String().
	mu               sync.Mutex
	closed           chan struct{}
}

func (s *Server) String() string {
	return fmt.Sprintf("dht server on %s", s.Socket.LocalAddr())
}

type Node struct {
	addr          *net.UDPAddr
	id            string
	lastHeardFrom time.Time
	lastSentTo    time.Time
}

func (n *Node) NodeInfo() (ret NodeInfo) {
	ret.Addr = n.addr
	if n := copy(ret.ID[:], n.id); n != 20 {
		panic(n)
	}
	return
}

func (n *Node) Good() bool {
	if len(n.id) != 20 {
		return false
	}
	if time.Now().Sub(n.lastHeardFrom) >= 15*time.Minute {
		return false
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

type transaction struct {
	remoteAddr net.Addr
	t          string
	Response   chan Msg
	onResponse func(Msg)
	done       chan struct{}
}

func (t *transaction) timeout() {
	close(t.Response)
	close(t.done)
}

func (t *transaction) handleResponse(m Msg) {
	close(t.done)
	if t.onResponse != nil {
		t.onResponse(m)
	}
	t.Response <- m
	close(t.Response)
}

func (s *Server) setDefaults() (err error) {
	if s.Socket == nil {
		var addr *net.UDPAddr
		addr, err = net.ResolveUDPAddr("", ":6882")
		if err != nil {
			return
		}
		s.Socket, err = net.ListenUDP("udp", addr)
		if err != nil {
			return
		}
	}
	if s.ID == "" {
		var id [20]byte
		h := crypto.SHA1.New()
		ss, err := os.Hostname()
		if err != nil {
			log.Print(err)
		}
		ss += s.Socket.LocalAddr().String()
		h.Write([]byte(ss))
		if b := h.Sum(id[:0:20]); len(b) != 20 {
			panic(len(b))
		}
		if len(id) != 20 {
			panic(len(id))
		}
		s.ID = string(id[:])
	}
	s.nodes = make(map[string]*Node, 10000)
	return
}

func (s *Server) Init() (err error) {
	err = s.setDefaults()
	if err != nil {
		return
	}
	s.closed = make(chan struct{})
	return
}

func (s *Server) Serve() error {
	for {
		var b [0x10000]byte
		n, addr, err := s.Socket.ReadFromUDP(b[:])
		if err != nil {
			return err
		}
		var d Msg
		err = bencode.Unmarshal(b[:n], &d)
		if err != nil {
			if se, ok := err.(*bencode.SyntaxError); !ok || se.Offset != 0 {
				log.Printf("%s: received bad krpc message: %s: %q", s, err, b[:n])
			}
			continue
		}
		s.mu.Lock()
		if d["y"] == "q" {
			s.handleQuery(addr, d)
			s.mu.Unlock()
			continue
		}
		t := s.findResponseTransaction(d.T(), addr)
		if t == nil {
			//log.Printf("unexpected message: %#v", d)
			s.mu.Unlock()
			continue
		}
		t.handleResponse(d)
		s.removeTransaction(t)
		id := ""
		if d["y"] == "r" {
			id = d["r"].(map[string]interface{})["id"].(string)
		}
		s.heardFromNode(addr, id)
		s.mu.Unlock()
	}
}

func (s *Server) AddNode(ni NodeInfo) {
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

func (s *Server) handleQuery(source *net.UDPAddr, m Msg) {
	args := m["a"].(map[string]interface{})
	s.heardFromNode(source, args["id"].(string))
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

func (s *Server) reply(addr *net.UDPAddr, t string, r map[string]interface{}) {
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
		panic(err)
	}
}

func (s *Server) heardFromNode(addr *net.UDPAddr, id string) {
	n := s.getNode(addr)
	if len(id) == 20 {
		n.id = id
	}
	n.lastHeardFrom = time.Now()
}

func (s *Server) getNode(addr *net.UDPAddr) (n *Node) {
	if addr.Port == 0 {
		panic(addr)
	}
	n = s.nodes[addr.String()]
	if n == nil {
		n = &Node{
			addr: addr,
		}
		s.nodes[addr.String()] = n
	}
	return
}

func (s *Server) writeToNode(b []byte, node *net.UDPAddr) (err error) {
	n, err := s.Socket.WriteTo(b, node)
	if err != nil {
		err = fmt.Errorf("error writing %d bytes to %s: %s", len(b), node, err)
		return
	}
	if n != len(b) {
		err = io.ErrShortWrite
		return
	}
	s.sentToNode(node)
	return
}

func (s *Server) sentToNode(addr *net.UDPAddr) {
	n := s.getNode(addr)
	n.lastSentTo = time.Now()
}

func (s *Server) findResponseTransaction(transactionID string, sourceNode net.Addr) *transaction {
	for _, t := range s.transactions {
		if t.t == transactionID && t.remoteAddr.String() == sourceNode.String() {
			return t
		}
	}
	return nil
}

func (s *Server) nextTransactionID() string {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], s.transactionIDInt)
	s.transactionIDInt++
	return string(b[:n])
}

func (s *Server) removeTransaction(t *transaction) {
	for i, tt := range s.transactions {
		if t == tt {
			last := len(s.transactions) - 1
			s.transactions[i] = s.transactions[last]
			s.transactions = s.transactions[:last]
			return
		}
	}
	panic("transaction not found")
}

func (s *Server) addTransaction(t *transaction) {
	s.transactions = append(s.transactions, t)
}

func (s *Server) IDString() string {
	if len(s.ID) != 20 {
		panic("bad node id")
	}
	return s.ID
}

func (s *Server) timeoutTransaction(t *transaction) {
	select {
	case <-t.done:
		return
	case <-time.After(time.Minute):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-t.done:
		return
	default:
	}
	t.timeout()
	s.removeTransaction(t)
}

func (s *Server) query(node *net.UDPAddr, q string, a map[string]string) (t *transaction, err error) {
	tid := s.nextTransactionID()
	if a == nil {
		a = make(map[string]string, 1)
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
		remoteAddr: node,
		t:          tid,
		Response:   make(chan Msg, 1),
		done:       make(chan struct{}),
	}
	s.addTransaction(t)
	err = s.writeToNode(b, node)
	if err != nil {
		s.removeTransaction(t)
		return
	}
	go s.timeoutTransaction(t)
	return
}

const CompactNodeInfoLen = 26

type NodeInfo struct {
	ID   [20]byte
	Addr *net.UDPAddr
}

func (ni *NodeInfo) PutCompact(b []byte) error {
	if n := copy(b[:], ni.ID[:]); n != 20 {
		panic(n)
	}
	ip := ni.Addr.IP.To4()
	if len(ip) != 4 {
		panic(ip)
	}
	if n := copy(b[20:], ip); n != 4 {
		panic(n)
	}
	binary.BigEndian.PutUint16(b[24:], uint16(ni.Addr.Port))
	return nil
}

func (cni *NodeInfo) UnmarshalCompact(b []byte) error {
	if len(b) != 26 {
		return errors.New("expected 26 bytes")
	}
	if 20 != copy(cni.ID[:], b[:20]) {
		panic("impossibru!")
	}
	if cni.Addr == nil {
		cni.Addr = &net.UDPAddr{}
	}
	cni.Addr.IP = net.IPv4(b[20], b[21], b[22], b[23])
	cni.Addr.Port = int(binary.BigEndian.Uint16(b[24:26]))
	return nil
}

func (s *Server) Ping(node *net.UDPAddr) (*transaction, error) {
	return s.query(node, "ping", nil)
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
			if cni.Addr.Port == 0 {
				// TODO: Why would people even do this?
				continue
			}
			n := s.getNode(cni.Addr)
			n.id = string(cni.ID[:])
		}
		// log.Printf("lifted %d nodes", len(r.Nodes))
	}
}

// Sends a find_node query to addr. targetID is the node we're looking for.
func (s *Server) findNode(addr *net.UDPAddr, targetID string) (t *transaction, err error) {
	t, err = s.query(addr, "find_node", map[string]string{"target": targetID})
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

type getPeersResponse struct {
	Values []tracker.CompactPeer `bencode:"values"`
	Nodes  util.CompactPeers     `bencode:"nodes"`
}

type peerStream struct {
	mu     sync.Mutex
	Values chan []tracker.CompactPeer
	stop   chan struct{}
}

func (ps *peerStream) Close() {
	ps.mu.Lock()
	select {
	case <-ps.stop:
	default:
		close(ps.stop)
		close(ps.Values)
	}
	ps.mu.Unlock()
}

func extractValues(m Msg) (vs []tracker.CompactPeer) {
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
	// log.Fatal(m)
	vl, ok := v.([]interface{})
	if !ok {
		panic(v)
	}
	vs = make([]tracker.CompactPeer, 0, len(vl))
	for _, i := range vl {
		// log.Printf("%T", i)
		s, ok := i.(string)
		if !ok {
			panic(i)
		}
		var cp tracker.CompactPeer
		err := cp.UnmarshalBinary([]byte(s))
		if err != nil {
			log.Printf("error decoding values list element: %s", err)
			continue
		}
		vs = append(vs, cp)
	}
	return
}

func (s *Server) GetPeers(infoHash string) (ps *peerStream, err error) {
	ps = &peerStream{
		Values: make(chan []tracker.CompactPeer),
		stop:   make(chan struct{}),
	}
	done := make(chan struct{})
	pending := 0
	s.mu.Lock()
	for _, n := range s.closestGoodNodes(160, infoHash) {
		var t *transaction
		t, err = s.getPeers(n.addr, infoHash)
		if err != nil {
			ps.Close()
			break
		}
		go func() {
			select {
			case m := <-t.Response:
				vs := extractValues(m)
				if vs != nil {
					select {
					case ps.Values <- vs:
					case <-ps.stop:
					}
				}
			case <-ps.stop:
			}
			done <- struct{}{}
		}()
		pending++
	}
	s.mu.Unlock()
	go func() {
		for ; pending > 0; pending-- {
			select {
			case <-done:
			case <-s.closed:
			}
		}
		ps.Close()
	}()
	return
}

func (s *Server) getPeers(addr *net.UDPAddr, infoHash string) (t *transaction, err error) {
	if len(infoHash) != 20 {
		err = fmt.Errorf("infohash has bad length")
		return
	}
	t, err = s.query(addr, "get_peers", map[string]string{"info_hash": infoHash})
	if err != nil {
		return
	}
	t.setOnResponse(func(m Msg) {
		s.liftNodes(m)
	})
	return
}

func (s *Server) addRootNode() error {
	addr, err := net.ResolveUDPAddr("udp4", "router.bittorrent.com:6881")
	if err != nil {
		return err
	}
	s.nodes[addr.String()] = &Node{
		addr: addr,
	}
	return nil
}

// Populates the node table.
func (s *Server) Bootstrap() (err error) {
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
			t, err = s.findNode(node.addr, s.ID)
			if err != nil {
				return
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
		if n.Good() {
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

func (s *Server) StopServing() {
	s.Socket.Close()
	s.mu.Lock()
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	s.mu.Unlock()
}

func idDistance(a, b string) (ret int) {
	if len(a) != 20 {
		panic(a)
	}
	if len(b) != 20 {
		panic(b)
	}
	for i := 0; i < 20; i++ {
		for j := uint(0); j < 8; j++ {
			ret += int(a[i]>>j&1 ^ b[i]>>j&1)
		}
	}
	return
}

func (s *Server) closestGoodNodes(k int, targetID string) []*Node {
	sel := newKClosestNodesSelector(k, targetID)
	idNodes := make(map[string]*Node, len(s.nodes))
	for _, node := range s.nodes {
		if !node.Good() {
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
