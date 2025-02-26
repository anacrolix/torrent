package torrent

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/sync"
	"github.com/anacrolix/torrent/metainfo"
)

var lpd *LPDServer
var mu sync.Mutex

// http://bittorrent.org/beps/bep_0014.html
// TODO http://bittorrent.org/beps/bep_0026.html

const (
	bep14_host4    = "239.192.152.143:6771"
	bep14_host6    = "[ff15::efc0:988f]:6771"
	bep14_announce = "BT-SEARCH * HTTP/1.1\r\n" +
		"Host: %s\r\n" +
		"Port: %s\r\n" +
		"%s" +
		"\r\n" +
		"\r\n"
	bep14_announce_infohash = "Infohash: %s\r\n"
	bep14_long_timeout      = 1 * time.Minute
	bep14_short_timeout     = 2 * time.Second // bep14 - 1 minute. not practial. what if use start/stop another torrent? so make it 2 secs.
	bep14_max               = 0               // maximum hashes per request, 0 - only limited by udp packet size
)

type LPDConn struct {
	stop  missinggo.Event
	force missinggo.Event

	network     string // "udp4" or "udp6"
	addr        *net.UDPAddr
	mcListener  *net.UDPConn
	mcPublisher *net.UDPConn
	host        string // bep14_host4 or bep14_host6
}

func lpdConnNew(network string, host string) *LPDConn {
	m := &LPDConn{}

	m.network = network
	m.host = host

	var err error

	m.addr, err = net.ResolveUDPAddr(m.network, m.host)
	if err != nil {
		log.Println("LPD unable to start", err)
		return nil
	}
	m.mcListener, err = net.ListenMulticastUDP(m.network, nil, m.addr)
	if err != nil {
		log.Println("LPD unable to start", err)
		return nil
	}
	m.mcPublisher, err = net.DialUDP(network, nil, m.addr)
	if err != nil {
		fmt.Println("Error dialing UDP:", err)
		return nil
	}

	return m
}

func (m *LPDConn) receiver(client *Client) {
	for {
		mu.Lock()
		conn := m.mcListener
		if conn == nil {
			mu.Unlock()
			return
		}
		mu.Unlock()

		buf := make([]byte, 2000)
		_, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Println("receiver", err)
			continue
		}

		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf)))
		if err != nil {
			log.Println("receiver", err)
			continue
		}

		if req.Method != "BT-SEARCH" {
			log.Println("receiver", "Wrong request: ", req.Method)
			continue
		}

		// bep14 says here can be multiple response headers
		var ihs []string = req.Header[http.CanonicalHeaderKey("Infohash")]
		if ihs == nil {
			log.Println("receiver", "No Infohash")
			continue
		}

		port := req.Header.Get("Port")
		if port == "" {
			log.Println("receiver", "No port")
			continue
		}

		addr, err := net.ResolveUDPAddr(m.network, net.JoinHostPort(from.IP.String(), port))
		if err != nil {
			log.Println("receiver", err)
			continue
		}

		mu.Lock()
		if lpd == nil { // can be closed already
			mu.Unlock()
			return
		}
		lpd.peer(addr.String())
		lpd.refresh()
		//log.Println("LPD", m.network, addr.String(), ih)
		ignore := make(map[*Torrent]bool)
		for _, ih := range ihs {
			hash := metainfo.NewHashFromHex(ih)
			if t, ok := client.Torrent(hash); ok {
				lpdPeer(t, addr.String())
				ignore[t] = true
			}
		}
		// LPD is the only source of local IP's. So, add it to all active torrents.
		for t := range client.torrents {
			if _, ok := ignore[t]; ok {
				continue
			}
			lpdPeer(t, addr.String())
		}
		mu.Unlock()
	}
}

func (m *LPDConn) announcer(client *Client) {
	var refresh time.Duration = 0
	var next *Torrent
	var queue []*Torrent

	for {
		mu.Lock()
		m.force.Clear()
		mu.Unlock()

		//log.Println("LPD", refresh)

		select {
		case <-m.stop.LockedChan(&mu):
			return
		case <-m.force.LockedChan(&mu):
		case <-time.After(refresh):
		}

		mu.Lock()
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
		lpd.refresh()

		var ihs string
		var old []byte

		port := client.LocalPort()
		count := 0
		for next != nil {
			ihs += fmt.Sprintf(bep14_announce_infohash, strings.ToUpper(next.InfoHash().HexString()))
			req := fmt.Sprintf(bep14_announce, m.host, strconv.Itoa(port), ihs)
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
			if bep14_max > 0 && count >= bep14_max {
				break
			}
		}
		mu.Unlock()

		if len(old) > 0 {
			//log.Println("LPD", string(old), len(old))
			_, err := m.mcPublisher.Write(old)
			if err != nil {
				log.Println("announcer", err)
			}
		}

		refresh = bep14_short_timeout
		if next == nil { // restart queue
			refresh = bep14_long_timeout
		}
	}
}

func (m *LPDConn) Close() {
	m.stop.Set()
	if m.mcListener != nil {
		m.mcListener.Close()
		m.mcListener = nil
	}
}

type LPDServer struct {
	conn4 *LPDConn
	conn6 *LPDConn

	peers map[int64]string // active local peers
}

func lpdStart(client *Client) {
	lpd = &LPDServer{}

	lpd.peers = make(map[int64]string)

	lpd.conn4 = lpdConnNew("udp4", bep14_host4)
	if lpd.conn4 != nil {
		go lpd.conn4.receiver(client)
		go lpd.conn4.announcer(client)
	}

	lpd.conn6 = lpdConnNew("udp6", bep14_host6)
	if lpd.conn6 != nil {
		go lpd.conn6.receiver(client)
		go lpd.conn6.announcer(client)
	}
}

func (m *LPDServer) refresh() {
	now := time.Now().UnixNano()
	var remove []int64
	for t := range m.peers {
		// remove old peers who did not refresh for 2 * bep14_long_timeout
		if t+(2*bep14_long_timeout).Nanoseconds() < now {
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

func lpdForce() {
	if lpd.conn4 != nil {
		lpd.conn4.force.Set()
	}
	if lpd.conn6 != nil {
		lpd.conn6.force.Set()
	}
}

func lpdStop() {
	mu.Lock()
	defer mu.Unlock()
	if lpd != nil {
		if lpd.conn4 != nil {
			lpd.conn4.Close()
			lpd.conn4 = nil
		}
		if lpd.conn6 != nil {
			lpd.conn6.Close()
			lpd.conn6 = nil
		}
		lpd = nil
	}
}

func lpdPeers(t *Torrent) {
	for _, p := range lpd.peers {
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
		Source: PeerSourceDhtGetPeers,
	}
	t.AddPeers([]PeerInfo{peer})
}
