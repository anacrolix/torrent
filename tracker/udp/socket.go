package udp

import (
	"errors"
	"log/slog"
	"net"
	"sync"
)

// Socket is a UDP packet conn that any number of tracker clients can share. Responses are matched
// to requests by transaction ID, so one socket can serve every tracker for a network. The
// underlying conn is opened on first use, and opened again on the next use if reading from it
// fails. The zero value is ready to use, and listens on "udp".
type Socket struct {
	// The network to listen on, such as "udp4", "udp6" or "udp".
	Network string
	// Substitute for net.ListenPacket.
	ListenPacket func(network, addr string) (net.PacketConn, error)
	// Logger for internal errors.
	Logger *slog.Logger

	mu     sync.Mutex
	conn   net.PacketConn
	closed bool
	d      Dispatcher
}

var errSocketClosed = errors.New("udp tracker socket closed")

func (s *Socket) network() string {
	if s.Network == "" {
		return "udp"
	}
	return s.Network
}

func (s *Socket) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// Returns the open conn, opening it if necessary.
func (s *Socket) getConn() (net.PacketConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errSocketClosed
	}
	if s.conn != nil {
		return s.conn, nil
	}
	listen := s.ListenPacket
	if listen == nil {
		listen = net.ListenPacket
	}
	conn, err := listen(s.network(), ":0")
	if err != nil {
		return nil, err
	}
	s.logger().Debug("new udp tracker client packet conn", "local addr", conn.LocalAddr())
	s.conn = conn
	go s.reader(conn)
	return conn, nil
}

func (s *Socket) reader(conn net.PacketConn) {
	b := make([]byte, 0x800)
	for {
		n, addr, err := conn.ReadFrom(b)
		if err != nil {
			s.mu.Lock()
			if s.conn == conn {
				// Don't panic, just drop the conn. The next request opens a new one. See
				// https://github.com/anacrolix/torrent/issues/845.
				s.conn = nil
			}
			closed := s.closed
			s.mu.Unlock()
			conn.Close()
			if !closed {
				s.logger().Debug("reading udp tracker packet conn", "err", err)
			}
			return
		}
		err = s.d.Dispatch(b[:n], addr)
		if err != nil {
			s.logger().Debug("error dispatching received packet",
				"source addr", addr,
				"err", err,
			)
		}
	}
}

// LocalAddr returns the local address of the underlying conn, or nil if it isn't open.
func (s *Socket) LocalAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

// Close closes the underlying conn. Requests through a closed Socket fail.
func (s *Socket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

// Allows a UDP Client to write packets to an endpoint without knowing about the network specifics.
type socketWriter struct {
	s       *Socket
	address string
}

func (me socketWriter) Write(p []byte) (n int, err error) {
	addr, err := net.ResolveUDPAddr(me.s.network(), me.address)
	if err != nil {
		return
	}
	pc, err := me.s.getConn()
	if err != nil {
		return
	}
	return pc.WriteTo(p, addr)
}
