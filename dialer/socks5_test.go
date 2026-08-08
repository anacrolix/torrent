package dialer

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func ExampleNewSocks5() {
	d := NewSocks5("localhost:1080", nil)
	// The dialer can be added to a client for outgoing peer connections:
	// cl.AddDialer(d)
	_ = d
}

// startSocks5TestServer starts a minimal SOCKS5 (RFC 1928) server that
// supports the no-authentication method and CONNECT requests. The returned
// address is where the proxy listens; target is where connections are
// forwarded.
func startSocks5TestServer(t *testing.T, target net.Listener) (proxyAddr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				if err := handleSocks5Conn(conn, target.Addr().String()); err != nil {
					conn.Close()
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func handleSocks5Conn(conn net.Conn, targetAddr string) error {
	defer conn.Close()
	// Greeting: version, nmethods, methods.
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting[0] != 5 {
		return fmt.Errorf("unexpected SOCKS version %d", greeting[0])
	}
	methods := make([]byte, greeting[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	// Respond with no-authentication required.
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return err
	}

	// Request: version, cmd, rsv, atyp, addr, port.
	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil {
		return err
	}
	if req[0] != 5 || req[1] != 1 {
		return fmt.Errorf("unsupported request v=%d cmd=%d", req[0], req[1])
	}
	var host string
	switch req[3] {
	case 1: // IPv4
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	case 3: // Domain name
		var lenb [1]byte
		if _, err := io.ReadFull(conn, lenb[:]); err != nil {
			return err
		}
		hostb := make([]byte, lenb[0])
		if _, err := io.ReadFull(conn, hostb); err != nil {
			return err
		}
		host = string(hostb)
	case 4: // IPv6
		ip := make([]byte, 16)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	default:
		return fmt.Errorf("unsupported address type %d", req[3])
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return err
	}
	address := net.JoinHostPort(host, fmt.Sprintf("%d", binaryBigEndianUint16(port[:])))
	targetConn, err := net.Dial("tcp", address)
	if err != nil {
		conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return err
	}
	defer targetConn.Close()
	// Success reply: v5, success, rsv, IPv4 0.0.0.0:0.
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	// Bidirectional relay.
	done := make(chan struct{}, 2)
	relay := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go relay(conn, targetConn)
	go relay(targetConn, conn)
	<-done
	return nil
}

func binaryBigEndianUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

func TestSocks5Dial(t *testing.T) {
	// A target "server" that echoes what it receives.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				io.Copy(conn, conn)
			}(conn)
		}
	}()

	proxyAddr := startSocks5TestServer(t, target)
	d := NewSocks5(proxyAddr, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := d.Dial(ctx, target.Addr().String())
	if err != nil {
		t.Fatalf("dial through SOCKS5 proxy: %v", err)
	}
	defer conn.Close()

	payload := []byte("hello through socks5")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo mismatch: got %q, want %q", buf, payload)
	}
}

func TestSocks5DialError(t *testing.T) {
	d := NewSocks5("127.0.0.1:1", nil) // Nothing listening on port 1.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := d.Dial(ctx, "127.0.0.1:0"); err == nil {
		t.Fatal("expected an error dialing an unreachable proxy")
	}
}

func TestSocks5DialerNetwork(t *testing.T) {
	if got := NewSocks5("127.0.0.1:1080", nil).DialerNetwork(); got != "tcp" {
		t.Fatalf("DialerNetwork() = %q, want %q", got, "tcp")
	}
}
