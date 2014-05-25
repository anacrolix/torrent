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
	"time"
)

type Server struct {
	ID               string
	Socket           *net.UDPConn
	transactions     []*transaction
	transactionIDInt uint64
	nodes            map[string]*Node
}

type Node struct {
	addr          *net.UDPAddr
	id            string
	lastHeardFrom time.Time
	lastSentTo    time.Time
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

func (s *Server) WriteNodes(w io.Writer) (n int, err error) {
	for _, node := range s.nodes {
		cni := compactNodeInfo{
			Addr: node.addr,
		}
		if n := copy(cni.ID[:], node.id); n != 20 {
			panic(n)
		}
		var b [26]byte
		cni.PutBinary(b[:])
		var nn int
		nn, err = w.Write(b[:])
		if err != nil {
			return
		}
		n += nn
	}
	return
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
	s.nodes = make(map[string]*Node, 1000)
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
		t := s.findResponseTransaction(d["t"].(string), addr)
		t.response <- d
		s.removeTransaction(t)
		id := ""
		if d["y"] == "r" {
			id = d["r"].(map[string]interface{})["id"].(string)
		}
		s.heardFromNode(addr, id)
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

const compactAddrInfoLen = 26

type compactAddrInfo *net.UDPAddr

type compactNodeInfo struct {
	ID   [20]byte
	Addr compactAddrInfo
}

func (cni *compactNodeInfo) PutBinary(b []byte) {
	if n := copy(b[:], cni.ID[:]); n != 20 {
		panic(n)
	}
	ip := cni.Addr.IP.To4()
	if len(ip) != 4 {
		panic(ip)
	}
	if n := copy(b[20:], ip); n != 4 {
		panic(n)
	}
	binary.BigEndian.PutUint16(b[24:], uint16(cni.Addr.Port))
}

func (cni *compactNodeInfo) UnmarshalBinary(b []byte) error {
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
	Nodes []compactNodeInfo
}

func (me *findNodeResponse) UnmarshalKRPCMsg(m Msg) error {
	b := m["r"].(map[string]interface{})["nodes"].(string)
	log.Printf("%q", b)
	for i := 0; i < len(b); i += 26 {
		var n compactNodeInfo
		err := n.UnmarshalBinary([]byte(b[i : i+26]))
		if err != nil {
			return err
		}
		me.Nodes = append(me.Nodes, n)
	}
	return nil
}

func (s *Server) FindNode(addr *net.UDPAddr, targetID string) (t *transaction, err error) {
	log.Print(addr)
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
				for _, cni := range r.Nodes {
					n := s.getNode(cni.Addr)
					n.id = string(cni.ID[:])
				}
			}
		}
		t.Response <- d
	}()
	return
}

func (s *Server) Bootstrap() error {
	if len(s.nodes) == 0 {
		addr, err := net.ResolveUDPAddr("udp4", "router.bittorrent.com:6881")
		if err != nil {
			return err
		}
		s.nodes[addr.String()] = &Node{
			addr: addr,
		}
	}
	queriedNodes := make(map[string]bool, 1000)
	for {
		for _, node := range s.nodes {
			if queriedNodes[node.addr.String()] {
				log.Printf("skipping already queried: %s", node.addr)
				continue
			}
			t, err := s.FindNode(node.addr, s.ID)
			if err != nil {
				return err
			}
			queriedNodes[node.addr.String()] = true
			go func() {
				log.Print(<-t.Response)
			}()
		}
		time.Sleep(3 * time.Second)
	}
	return nil
}
