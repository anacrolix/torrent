package dht

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/nsf/libtorgo/bencode"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

type Server struct {
	ID               string
	Socket           *net.UDPConn
	transactions     []*transaction
	transactionIDInt uint64
	nodes            map[string]*Node
	mu               sync.Mutex
}

type Node struct {
	addr          *net.UDPAddr
	id            string
	lastHeardFrom time.Time
	lastSentTo    time.Time
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

type transaction struct {
	remoteAddr net.Addr
	t          string
	Response   chan Msg
	response   chan Msg
}

func (s *Server) setDefaults() {
	if s.ID == "" {
		var id [20]byte
		_, err := rand.Read(id[:])
		if err != nil {
			panic(err)
		}
		s.ID = string(id[:])
	}
}

func (s *Server) Init() {
	s.setDefaults()
}

func (s *Server) Serve() error {
	for {
		var b [1500]byte
		n, addr, err := s.Socket.ReadFromUDP(b[:])
		if err != nil {
			return err
		}
		var d map[string]interface{}
		err = bencode.Unmarshal(b[:n], &d)
		if err != nil {
			log.Printf("bad krpc message: %s", err)
			continue
		}
		s.mu.Lock()
		if d["y"] == "q" {
			s.handleQuery(addr, d)
			s.mu.Unlock()
			continue
		}
		t := s.findResponseTransaction(d["t"].(string), addr)
		if t == nil {
			log.Printf("unexpected message: %#v", d)
			s.mu.Unlock()
			continue
		}
		t.response <- d
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

func (s *Server) handleQuery(source *net.UDPAddr, m Msg) {
	log.Print(m["q"])
	if m["q"] != "ping" {
		return
	}
	s.heardFromNode(source, m["a"].(map[string]interface{})["id"].(string))
	s.reply(source, m["t"].(string))
}

func (s *Server) reply(addr *net.UDPAddr, t string) {
	m := map[string]interface{}{
		"t": t,
		"y": "r",
		"r": map[string]string{
			"id": s.IDString(),
		},
	}
	b, err := bencode.Marshal(m)
	if err != nil {
		panic(err)
	}
	_, err = s.Socket.WriteTo(b, addr)
	if err != nil {
		panic(err)
	}
}

func (s *Server) heardFromNode(addr *net.UDPAddr, id string) {
	n := s.getNode(addr)
	n.id = id
	n.lastHeardFrom = time.Now()
}

func (s *Server) getNode(addr *net.UDPAddr) (n *Node) {
	n = s.nodes[addr.String()]
	if n == nil {
		n = &Node{
			addr: addr,
		}
		s.nodes[addr.String()] = n
	}
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
	}
	t.response = t.Response
	s.addTransaction(t)
	n, err := s.Socket.WriteTo(b, node)
	if err != nil {
		s.removeTransaction(t)
		return
	}
	if n != len(b) {
		err = io.ErrShortWrite
		s.removeTransaction(t)
		return
	}
	s.sentToNode(node)
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

func (s *Server) FindNode(addr *net.UDPAddr, targetID string) (t *transaction, err error) {
	t, err = s.query(addr, "find_node", map[string]string{"target": targetID})
	if err != nil {
		return
	}
	ch := make(chan Msg)
	t.response = ch
	go func() {
		d, ok := <-t.response
		if !ok {
			close(t.Response)
			return
		}
		if d["y"] == "r" {
			var r findNodeResponse
			err = r.UnmarshalKRPCMsg(d)
			if err != nil {
				log.Print(err)
			} else {
				s.mu.Lock()
				for _, cni := range r.Nodes {
					n := s.getNode(cni.Addr)
					n.id = string(cni.ID[:])
				}
				s.mu.Unlock()
			}
		}
		t.Response <- d
	}()
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
		if err != nil {
			return
		}
	}
	for _, node := range s.nodes {
		var t *transaction
		s.mu.Unlock()
		t, err = s.FindNode(node.addr, s.ID)
		s.mu.Lock()
		if err != nil {
			return
		}
		go func() {
			<-t.Response
		}()
	}
	return
}

func (s *Server) GoodNodes() (nis []NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, node := range s.nodes {
		if !node.Good() {
			continue
		}
		ni := NodeInfo{
			Addr: node.addr,
		}
		if n := copy(ni.ID[:], node.id); n != 20 {
			panic(n)
		}
		nis = append(nis, ni)
	}
	return
}

func (s *Server) StopServing() {
	s.Socket.Close()
}
