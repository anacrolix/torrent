package torrent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/anacrolix/torrent/metainfo"
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
	// bep14 - 1 minute. not practical. what if use start/stop another torrent? so make it 2 secs.
	// TODO: Trigger this when torrents are added/removed instead, with a minimum delay (coalesce
	// frequent changes).
	bep14ShortTimeout = 2 * time.Second
	bep14Max          = 0 // maximum hashes per request, 0 - only limited by udp packet size
)

type lpdConn struct {
	stop  missinggo.Event
	force missinggo.Event

	lpd         *LPDServer
	network     string // "udp4" or "udp6"
	addr        *net.UDPAddr
	mcListener  *net.UDPConn
	mcPublisher *net.UDPConn
	host        string // bep14Host4 or bep14Host6
	closed      bool
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
	m := &lpdConn{}

	m.lpd = lpd
	m.network = network
	m.host = host
	m.logger = log.Default

	var err error

	m.addr, err = net.ResolveUDPAddr(m.network, m.host)
	if err != nil {
		m.logger.Println("LPD unable to start", err)
		return nil
	}
	m.mcListener, err = net.ListenMulticastUDP(m.network, nil, m.addr)
	if err != nil {
		m.logger.Println("LPD unable to start", err)
		return nil
	}

	m.mcPublisher, err = net.DialUDP(network, nil, m.addr)
	if err != nil {
		m.logger.Println("Error dialing UDP:", err)
		return nil
	}

	if config.Ifi != "" {
		iface, err := net.InterfaceByName(config.Ifi)
		if err != nil {
			m.logger.Printf("Interface error: %v\n", err)
			return nil
		}
		sourceUdpAddress, err := sourceUdpAddress(iface, network)
		if err != nil {
			m.logger.Printf("could not get source udp address: %v\n", err)
			return nil
		}
		m.mcPublisher, err = net.DialUDP(network, sourceUdpAddress, m.addr)
		if err != nil {
			m.logger.Println("Error dialing multicast interface:", err)
			return nil
		}
		err = setMulticastInterface(m, iface)
		if err != nil {
			m.logger.Println("Error setting multicast interface:", err)
			return nil
		}
	} else {
		m.mcPublisher, err = net.DialUDP(network, nil, m.addr)
		m.logger.Printf("Multicasting on %v\n", m.mcPublisher.LocalAddr().String())
		if err != nil {
			m.logger.Println("Error dialing multicast interface:", err)
			return nil
		}
	}

	return m
}

func (m *lpdConn) receiver(client *Client) {
	for {
		m.lpd.mu.RLock()
		if m.closed {
			m.lpd.mu.RUnlock()
			return
		}
		m.lpd.mu.RUnlock()

		buf := make([]byte, 2000)
		_, from, err := m.mcListener.ReadFromUDP(buf)
		if err != nil {
			m.lpd.mu.RLock()
			if m.closed {
				m.lpd.mu.RUnlock()
				return
			}
			m.lpd.mu.RUnlock()
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
		client.rLock()

		// Possible to receive own UDP multicast message, ignore it.
		if client.LocalPort() == addr.Port {
			client.rUnlock()
			continue
		}
		client.rUnlock()

		m.lpd.mu.Lock()
		if m.lpd == nil { // can be closed already
			m.lpd.mu.Unlock()
			return
		}

		m.lpd.peer(addr.String())
		m.lpd.refresh()
		m.lpd.mu.Unlock()

		ignore := make(map[*Torrent]bool)
		for _, ih := range ihs {
			log.Default.LevelPrint(log.Debug, "LPD", m.network, addr.String(), ih)
			hash := metainfo.NewHashFromHex(ih)
			if t, ok := client.Torrent(hash); ok {
				lpdPeer(t, addr.String())
				ignore[t] = true
			}
		}

		// LPD is the only source of local IPs. So add it to all active torrents.
		torrents := []*Torrent{}
		client.rLock()
		for t := range client.torrents {
			if _, ok := ignore[t]; ok {
				continue
			}
			torrents = append(torrents, t)
		}
		client.rUnlock()

		for _, t := range torrents {
			lpdPeer(t, addr.String())
		}
	}
}

func (m *lpdConn) announcer(client *Client) {
	var refresh time.Duration = 100 * time.Millisecond
	var next *Torrent
	var queue []*Torrent

	for {
		m.lpd.mu.Lock()
		m.force.Clear()
		m.lpd.mu.Unlock()

		select {
		case <-m.stop.LockedChan(&m.lpd.mu):
			return
		case <-m.force.LockedChan(&m.lpd.mu):
		case <-time.After(refresh):
		}

		m.lpd.mu.Lock()
		client.rLock()
		// add missing torrent to send queue
		for t := range client.torrents {
			if _, ok := lpdContains(queue, t); !ok {
				queue = append(queue, t)
			}
		}

		if next == nil {
			if len(queue) > 0 {
				next = queue[0]
			}
		}

		// remove stopped torrent from queue
		var remove []*Torrent
		for _, t := range queue {
			if _, ok := client.torrents[t]; !ok {
				remove = append(remove, t)
			}
		}

		for _, t := range remove {
			if i, ok := lpdContains(queue, t); ok {
				if next == t { // update next to next+1
					n := i + 1
					if n >= len(queue) {
						next = nil
					} else {
						next = queue[n]
					}
				}
				queue = append(queue[:i], queue[i+1:]...)
			}
		}
		m.lpd.refresh()

		var ihs string
		var old []byte

		port := client.LocalPort()
		client.rUnlock()
		count := 0
		for next != nil {
			ihs += fmt.Sprintf(bep14AnnounceInfohash, strings.ToUpper(next.InfoHash().HexString()))
			req := fmt.Sprintf(bep14Announce, m.host, strconv.Itoa(port), ihs)
			buf := []byte(req)
			if len(buf) >= 1400 {
				break
			}
			old = buf
			if i, ok := lpdContains(queue, next); ok {
				i++
				if i >= len(queue) {
					next = nil
				} else {
					next = queue[i]
				}
			}
			count++
			if bep14Max > 0 && count >= bep14Max {
				break
			}
		}
		m.lpd.mu.Unlock()

		if len(old) > 0 {
			//log.Println("LPD", string(old), len(old))
			_, err := m.mcPublisher.Write(old)
			if err != nil {
				m.logger.Println("announcer", err)
			}
		}

		refresh = bep14ShortTimeout
		if next == nil { // restart queue
			refresh = bep14LongTimeout
		}
	}
}

type LPDServer struct {
	mu    sync.RWMutex
	conn4 *lpdConn
	conn6 *lpdConn

	peers map[int64]string // active local peers
}

func (lpd *LPDServer) lpdStart(client *Client) {
	lpd.peers = make(map[int64]string)

	lpd.conn4 = lpdConnNew("udp4", bep14Host4, lpd, client.config.LocalServiceDiscoveryConfig)
	if lpd.conn4 != nil {
		go lpd.conn4.receiver(client)
		go lpd.conn4.announcer(client)
	}

	if client.config.LocalServiceDiscoveryConfig.Ip6 {
		lpd.conn6 = lpdConnNew("udp6", bep14Host6, lpd, client.config.LocalServiceDiscoveryConfig)
		if lpd.conn6 != nil {
			go lpd.conn6.receiver(client)
			go lpd.conn6.announcer(client)
		}
	}
}

func (m *LPDServer) refresh() {
	now := time.Now().UnixNano()
	var remove []int64
	for t := range m.peers {
		// remove old peers who did not refresh for 2 * bep14_long_timeout
		if t+(2*bep14LongTimeout).Nanoseconds() < now {
			remove = append(remove, t)
		}
	}
	for _, t := range remove {
		delete(m.peers, t)
	}
}

func (m *LPDServer) peer(peer string) {
	now := time.Now().UnixNano()
	var remove []int64
	for k, v := range m.peers {
		if v == peer {
			remove = append(remove, k)
		}
	}
	m.peers[now] = peer
	for _, v := range remove {
		delete(m.peers, v)
	}
}

func lpdContains(queue []*Torrent, e *Torrent) (int, bool) {
	for i, t := range queue {
		if t == e {
			return i, true
		}
	}
	return -1, false
}

func (lpd *LPDServer) lpdForce() {
	lpd.mu.Lock()
	defer lpd.mu.Unlock()
	if lpd.conn4 != nil {
		lpd.conn4.force.Set()
	}
	if lpd.conn6 != nil {
		lpd.conn6.force.Set()
	}
}

func (m *lpdConn) Close() {
	m.lpd.mu.Lock()
	m.stop.Set()
	m.closed = true
	m.lpd.mu.Unlock()

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
	peers := []string{}
	lpd.mu.RLock()
	for _, p := range lpd.peers {
		peers = append(peers, p)
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
	t.AddPeers([]PeerInfo{peer})
}
