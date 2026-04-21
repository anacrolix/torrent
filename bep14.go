package torrent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// http://bittorrent.org/beps/bep_0014.html
// TODO http://bittorrent.org/beps/bep_0026.html

const (
	bep14Host4    = "239.192.152.143:6771"
	bep14Host6    = "[ff15::efc0:988f]:6771"
	bep14Announce = "BT-SEARCH * HTTP/1.1\r\n" +
		"Host: %s\r\n" +
		"Port: %s\r\n" +
		"%s" +
		"\r\n" +
		"\r\n"
	bep14AnnounceInfohash = "Infohash: %s\r\n"
	bep14LongTimeout      = 10 * time.Second
	bep14ShortTimeout     = 1 * time.Second
	bep14MaxPacketSize    = 1400
)

// lpdClient is implemented by *Client and provides the hooks LPD goroutines
// need without requiring them to acquire or release client locks directly.
type lpdClient interface {
	// LocalPort returns the client's BitTorrent listen port.
	LocalPort() (port int)
	// OnLPDAnnouncement is called when a valid peer announcement is received.
	// addr is the peer's address ("host:port"); infohashes is the list of
	// announced infohash hex strings.
	OnLPDAnnouncement(addr string, infohashes []string)
	// TorrentInfohashesAndPort returns a snapshot of active torrent infohash
	// hex strings and the listen port, used for building announce packets.
	TorrentInfohashesAndPort() (port int, infohashes []string)
}

type lpdConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	force  chan struct{} // buffered(1): signal an immediate re-announce

	lpd         *LPDServer
	network     string // "udp4" or "udp6"
	addr        *net.UDPAddr
	mcListener  *net.UDPConn
	mcPublisher *net.UDPConn
	host        string // bep14Host4 or bep14Host6
	logger      log.Logger
}

func setMulticastInterface(m *lpdConn, iface *net.Interface) error {
	if m.network == "udp4" {
		p := ipv4.NewPacketConn(m.mcPublisher)
		if err := p.SetMulticastInterface(iface); err != nil {
			m.logger.Printf("Set multicast interface error: %v\n", err)
			return err
		}
	}
	if m.network == "udp6" {
		p := ipv6.NewPacketConn(m.mcPublisher)
		if err := p.SetMulticastInterface(iface); err != nil {
			m.logger.Printf("Set multicast interface error: %v\n", err)
			return err
		}
	}
	return nil
}

func sourceUdpAddress(iface *net.Interface, network string) (*net.UDPAddr, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		ip := ipNet.IP

		// Skip loopback and link-local addresses
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}

		switch network {
		case "udp4":
			if ip.To4() != nil {
				return &net.UDPAddr{IP: ip}, nil
			}
		case "udp6":
			if ip.To4() == nil && ip.To16() != nil {
				return &net.UDPAddr{IP: ip}, nil
			}
		default:
			return nil, errors.New("invalid network type, must be 'udp4' or 'udp6'")
		}
	}

	return nil, errors.New("no suitable IP address found")
}

func lpdConnNew(network string, host string, lpd *LPDServer, config LocalServiceDiscoveryConfig) *lpdConn {
	ctx, cancel := context.WithCancel(context.Background())
	m := &lpdConn{
		ctx:     ctx,
		cancel:  cancel,
		force:   make(chan struct{}, 1),
		lpd:     lpd,
		network: network,
		host:    host,
		logger:  log.Default,
	}

	var err error

	m.addr, err = net.ResolveUDPAddr(m.network, m.host)
	if err != nil {
		m.logger.Println("LPD unable to start", err)
		cancel()
		return nil
	}
	m.mcListener, err = net.ListenMulticastUDP(m.network, nil, m.addr)
	if err != nil {
		m.logger.Println("LPD unable to start", err)
		cancel()
		return nil
	}

	if config.Ifi != "" {
		iface, err := net.InterfaceByName(config.Ifi)
		if err != nil {
			m.logger.Printf("Interface error: %v\n", err)
			cancel()
			return nil
		}
		srcAddr, err := sourceUdpAddress(iface, network)
		if err != nil {
			m.logger.Printf("could not get source udp address: %v\n", err)
			cancel()
			return nil
		}
		m.mcPublisher, err = net.DialUDP(network, srcAddr, m.addr)
		if err != nil {
			m.logger.Println("Error dialing multicast interface:", err)
			cancel()
			return nil
		}
		if err = setMulticastInterface(m, iface); err != nil {
			m.logger.Println("Error setting multicast interface:", err)
			cancel()
			return nil
		}
	} else {
		m.mcPublisher, err = net.DialUDP(network, nil, m.addr)
		if err != nil {
			m.logger.Println("Error dialing UDP:", err)
			cancel()
			return nil
		}
		m.logger.Printf("Multicasting on %v\n", m.mcPublisher.LocalAddr().String())
	}

	return m
}

func (m *lpdConn) receiver(client lpdClient) {
	for {
		buf := make([]byte, 2000)
		_, from, err := m.mcListener.ReadFromUDP(buf)
		if err != nil {
			if m.ctx.Err() != nil {
				return // closed
			}
			m.logger.Println("receiver", err)
			continue
		}

		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf)))
		if err != nil {
			m.logger.Println("receiver", err)
			continue
		}

		m.logger.LevelPrint(log.Debug, "received, req: ", req)
		if req.Method != "BT-SEARCH" {
			m.logger.Println("receiver", "Wrong request: ", req.Method)
			continue
		}

		// BEP14 says here can be multiple response headers
		ihs := req.Header[http.CanonicalHeaderKey("Infohash")]
		if ihs == nil {
			m.logger.Println("receiver", "No Infohash")
			continue
		}

		port := req.Header.Get("Port")
		m.logger.LevelPrint(log.Debug, "received, port: ", port)
		if port == "" {
			m.logger.Println("receiver", "No port")
			continue
		}

		addr, err := net.ResolveUDPAddr(m.network, net.JoinHostPort(from.IP.String(), port))
		m.logger.LevelPrint(log.Debug, "received, addr: ", addr)
		if err != nil {
			m.logger.Println("receiver", err)
			continue
		}

		// Possible to receive own UDP multicast message, ignore it.
		publisherAddr := m.mcPublisher.LocalAddr().(*net.UDPAddr)
		if client.LocalPort() == addr.Port && from.IP.Equal(publisherAddr.IP) {
			m.logger.Println("receiver", "Ignoring own message")
			continue
		}

		m.lpd.mu.Lock()
		m.logger.Println("receiver", "Adding peer", addr.String())
		m.lpd.peer(addr.String())
		m.lpd.refresh()
		m.lpd.mu.Unlock()

		client.OnLPDAnnouncement(addr.String(), ihs)
	}
}

// buildAnnouncePacket builds a single BT-SEARCH announce packet starting at
// startIdx in queue. It returns the packet bytes, the index to resume from on
// the next tick, and whether the full queue has been covered (caller should
// use the long refresh interval when rotated is true).
func buildAnnouncePacket(host string, port int, queue []string, startIdx, maxSize int) (packet []byte, nextIdx int, rotated bool) {
	var ihs string
	nextIdx = startIdx
	for nextIdx < len(queue) {
		ihs += fmt.Sprintf(bep14AnnounceInfohash, strings.ToUpper(queue[nextIdx]))
		req := fmt.Sprintf(bep14Announce, host, strconv.Itoa(port), ihs)
		buf := []byte(req)
		if len(buf) >= maxSize {
			break
		}
		packet = buf
		nextIdx++
	}
	if nextIdx >= len(queue) {
		nextIdx = 0
		rotated = true
	}
	return
}

func (m *lpdConn) announcer(client lpdClient) {
	var (
		refresh = bep14LongTimeout
		queue   []string // infohash hex strings in announce rotation order
		nextIdx int
	)

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.force:
		case <-time.After(refresh):
		}

		port, current := client.TorrentInfohashesAndPort()

		// Sync queue: add torrents not yet present.
		inQueue := make(map[string]bool, len(queue))
		for _, h := range queue {
			inQueue[h] = true
		}
		for _, h := range current {
			if !inQueue[h] {
				queue = append(queue, h)
			}
		}

		// Remove torrents no longer active, keeping nextIdx consistent.
		activeSet := make(map[string]bool, len(current))
		for _, h := range current {
			activeSet[h] = true
		}
		newQueue := queue[:0]
		newNextIdx := nextIdx
		for i, h := range queue {
			if activeSet[h] {
				newQueue = append(newQueue, h)
			} else if i < nextIdx {
				newNextIdx--
			}
		}
		queue = newQueue
		nextIdx = newNextIdx
		if nextIdx > len(queue) {
			nextIdx = len(queue)
		}

		packet, newNextIdx, rotated := buildAnnouncePacket(m.host, port, queue, nextIdx, bep14MaxPacketSize)
		nextIdx = newNextIdx
		if rotated {
			refresh = bep14LongTimeout // completed a full rotation
		} else {
			refresh = bep14ShortTimeout // more torrents to announce next tick
		}

		if len(packet) > 0 {
			if _, err := m.mcPublisher.Write(packet); err != nil {
				if m.ctx.Err() != nil {
					return // closed
				}
				m.logger.Println("announcer", err)
			}
		}
	}
}

type LPDServer struct {
	mu    sync.RWMutex
	conn4 *lpdConn
	conn6 *lpdConn

	peers map[string]time.Time // addr -> last-seen time
}

func (lpd *LPDServer) lpdStart(client lpdClient, config LocalServiceDiscoveryConfig) {
	lpd.peers = make(map[string]time.Time)

	lpd.conn4 = lpdConnNew("udp4", bep14Host4, lpd, config)
	if lpd.conn4 != nil {
		go lpd.conn4.receiver(client)
		go lpd.conn4.announcer(client)
	}

	if config.Ip6 {
		lpd.conn6 = lpdConnNew("udp6", bep14Host6, lpd, config)
		if lpd.conn6 != nil {
			go lpd.conn6.receiver(client)
			go lpd.conn6.announcer(client)
		}
	}
}

func (m *LPDServer) refresh() {
	now := time.Now()
	for addr, lastSeen := range m.peers {
		if now.Sub(lastSeen) > 2*bep14LongTimeout {
			delete(m.peers, addr)
		}
	}
}

func (m *LPDServer) peer(addr string) {
	m.peers[addr] = time.Now()
}

func (lpd *LPDServer) lpdForce() {
	if lpd.conn4 != nil {
		select {
		case lpd.conn4.force <- struct{}{}:
		default:
		}
	}
	if lpd.conn6 != nil {
		select {
		case lpd.conn6.force <- struct{}{}:
		default:
		}
	}
}

func (m *lpdConn) Close() {
	m.cancel()
	m.mcListener.Close()
	m.mcPublisher.Close()
}

func (lpd *LPDServer) lpdStop() {
	if lpd != nil {
		if lpd.conn4 != nil {
			lpd.conn4.Close()
		}
		if lpd.conn6 != nil {
			lpd.conn6.Close()
		}
	}
}

func (lpd *LPDServer) lpdPeers(t *Torrent) {
	lpd.mu.RLock()
	peers := make([]string, 0, len(lpd.peers))
	for addr := range lpd.peers {
		peers = append(peers, addr)
	}
	lpd.mu.RUnlock()
	for _, p := range peers {
		lpdPeer(t, p)
	}
}

func lpdPeer(t *Torrent, p string) {
	host, port, err := net.SplitHostPort(p)
	if err != nil {
		return
	}
	pi, err := strconv.Atoi(port)
	if err != nil {
		return
	}
	ip := net.ParseIP(host)
	peer := PeerInfo{
		Addr:   &net.UDPAddr{IP: ip, Port: pi},
		Source: PeerSourceLPD,
	}
	t.logger.Println("lpdPeer", "Adding peer", peer.Addr.String())
	t.AddPeers([]PeerInfo{peer})
}
