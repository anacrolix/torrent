package dht

import (
	_ "crypto/sha1"
	"errors"
	"fmt"
	"hash/crc32"
	"math/big"
	"math/rand"
	"net"
	"strconv"
	"time"

	"github.com/anacrolix/torrent/iplist"
)

const (
	maxNodes = 320
)

var (
	queryResendEvery = 5 * time.Second
)

var maxDistance big.Int

func init() {
	var zero big.Int
	maxDistance.SetBit(&zero, 160, 1)
}

// Uniquely identifies a transaction to us.
type transactionKey struct {
	RemoteAddr string // host:port
	T          string // The KRPC transaction ID.
}

// ServerConfig allows to set up a  configuration of the `Server` instance
// to be created with NewServer
type ServerConfig struct {
	// Listen address. Used if Conn is nil.
	Addr string
	Conn net.PacketConn
	// Don't respond to queries from other nodes.
	Passive bool
	// DHT Bootstrap nodes
	BootstrapNodes []string
	// Disable bootstrapping from global servers even if given no BootstrapNodes.
	// This creates a solitary node that awaits other nodes; it's only useful if
	// you're creating your own DHT and want to avoid accidental crossover, without
	// spoofing a bootstrap node and filling your logs with connection errors.
	NoDefaultBootstrap bool

	// Disable the DHT security extension:
	// http://www.libtorrent.org/dht_sec.html.
	NoSecurity bool
	// Initial IP blocklist to use. Applied before serving and bootstrapping
	// begins.
	IPBlocklist iplist.Ranger
	// Used to secure the server's ID. Defaults to the Conn's LocalAddr().
	PublicIP net.IP
}

// ServerStats instance is returned by Server.Stats() and stores Server metrics
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

func makeSocket(addr string) (socket *net.UDPConn, err error) {
	addr_, err := net.ResolveUDPAddr("", addr)
	if err != nil {
		return
	}
	socket, err = net.ListenUDP("udp", addr_)
	return
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

type node struct {
	addr          dHTAddr
	id            nodeID
	announceToken string

	lastGotQuery    time.Time
	lastGotResponse time.Time
	lastSentQuery   time.Time
}

func (n *node) IsSecure() bool {
	if n.id.IsUnset() {
		return false
	}
	return NodeIdSecure(n.id.ByteString(), n.addr.IP())
}

func (n *node) idString() string {
	return n.id.ByteString()
}

func (n *node) SetIDFromBytes(b []byte) {
	if len(b) != 20 {
		panic(b)
	}
	n.id.i.SetBytes(b)
	n.id.set = true
}

func (n *node) SetIDFromString(s string) {
	n.SetIDFromBytes([]byte(s))
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

func jitterDuration(average time.Duration, plusMinus time.Duration) time.Duration {
	return average - plusMinus/2 + time.Duration(rand.Int63n(int64(plusMinus)))
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

type Peer struct {
	IP   net.IP
	Port int
}

func (me *Peer) String() string {
	return net.JoinHostPort(me.IP.String(), strconv.FormatInt(int64(me.Port), 10))
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
