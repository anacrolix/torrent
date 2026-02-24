package torrent

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/anacrolix/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSocket struct {
	addr net.Addr
}

func (m mockSocket) Accept() (net.Conn, error) { panic("not implemented") }
func (m mockSocket) Addr() net.Addr             { return m.addr }
func (m mockSocket) Close() error               { return nil }
func (m mockSocket) Dial(_ context.Context, _ string) (net.Conn, error) {
	panic("not implemented")
}
func (m mockSocket) DialerNetwork() string { panic("not implemented") }

// TestListenAllRetryPermissionError verifies that listenAllRetry retries when a subsequent listen
// fails with a permission error (EACCES) and the port is dynamic. This reproduces the Windows
// Server 2025 issue where Hyper-V/WinNAT reserves port ranges asymmetrically between TCP and UDP,
// causing the UDP bind to fail with WSAEACCES (mapped to EACCES) after TCP succeeds.
func TestListenAllRetryPermissionError(t *testing.T) {
	callCount := 0
	mockListen := func(n network, addr string, f firewallCallback, logger log.Logger) (socket, error) {
		callCount++
		if callCount == 1 {
			return mockSocket{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}}, nil
		}
		return nil, &os.SyscallError{
			Syscall: "bind",
			Err:     syscall.EACCES,
		}
	}

	nahs := []networkAndHost{
		{Network: network{Tcp: true, Ipv4: true}, Host: "127.0.0.1"},
		{Network: network{Udp: true, Ipv4: true}, Host: "127.0.0.1"},
	}

	ss, retry, err := listenAllRetry(nahs, 0, nil, log.Default, mockListen)
	require.Error(t, err)
	assert.Nil(t, ss, "sockets should be cleaned up on retry")
	assert.True(t, retry, "should retry when subsequent listen fails with EACCES on dynamic port")
}

// TestListenAllRetryFirstListenFails verifies that listenAllRetry does NOT retry when the first
// listen fails, regardless of error type, since no port has been established yet.
func TestListenAllRetryFirstListenFails(t *testing.T) {
	mockListen := func(n network, addr string, f firewallCallback, logger log.Logger) (socket, error) {
		return nil, &os.SyscallError{
			Syscall: "bind",
			Err:     syscall.EACCES,
		}
	}

	nahs := []networkAndHost{
		{Network: network{Tcp: true, Ipv4: true}, Host: "127.0.0.1"},
	}

	ss, retry, err := listenAllRetry(nahs, 0, nil, log.Default, mockListen)
	require.Error(t, err)
	assert.Nil(t, ss)
	assert.False(t, retry, "should not retry when first listen fails")
}

// TestListenAllRetryLimit verifies that listenAll gives up after the retry limit when the
// subsequent listen always fails. Without a limit this would loop infinitely.
func TestListenAllRetryLimit(t *testing.T) {
	callCount := 0
	mockListen := func(n network, addr string, f firewallCallback, logger log.Logger) (socket, error) {
		callCount++
		if callCount%2 == 1 {
			// First listen in each attempt succeeds.
			return mockSocket{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}}, nil
		}
		// Second listen always fails.
		return nil, &os.SyscallError{
			Syscall: "bind",
			Err:     syscall.EACCES,
		}
	}

	networks := []network{
		{Tcp: true, Ipv4: true},
		{Udp: true, Ipv4: true},
	}

	ss, err := listenAllWithListenFunc(
		networks,
		func(string) string { return "127.0.0.1" },
		0, nil, log.Default, mockListen,
	)
	require.Error(t, err)
	assert.Nil(t, ss)
	// Each attempt calls listen twice (first succeeds, second fails), so expect 2 * limit calls.
	expectedCalls := 2 * listenAllRetryLimit
	assert.Equal(t, expectedCalls, callCount, "should stop retrying after the limit")
}
