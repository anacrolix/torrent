package dht

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"github.com/nsf/libtorgo/bencode"
	"io"
	"log"
	"net"
	"time"
)

type Server struct {
	ID               string
	Socket           net.PacketConn
	transactions     []*transaction
	transactionIDInt uint64
	nodes            map[string]*Node
}

type Node struct {
	addr          net.Addr
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

func (s *Server) init() {
	s.nodes = make(map[string]*Node, 1000)
}

func (s *Server) Serve() error {
	s.setDefaults()
	s.init()
	for {
		var b [1500]byte
		n, addr, err := s.Socket.ReadFrom(b[:])
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
		t.Response <- d
		s.removeTransaction(t)
		id := ""
		if d["y"] == "r" {
			id = d["r"].(map[string]interface{})["id"].(string)
		}
		s.heardFromNode(addr, id)
	}
}

func (s *Server) heardFromNode(addr net.Addr, id string) {
	n := s.getNode(addr)
	n.id = id
	n.lastHeardFrom = time.Now()
}

func (s *Server) getNode(addr net.Addr) (n *Node) {
	n = s.nodes[addr.String()]
	if n == nil {
		n = &Node{
			addr: addr,
		}
		s.nodes[addr.String()] = n
	}
	return
}

func (s *Server) sentToNode(addr net.Addr) {
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

func (s *Server) query(node net.Addr, q string, a map[string]string) (t *transaction, err error) {
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

func (s *Server) GetPeers(node *net.UDPAddr, targetInfoHash [20]byte) {

}

func (s *Server) Ping(node net.Addr) (*transaction, error) {
	return s.query(node, "ping", nil)
}
