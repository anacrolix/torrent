package connections

import (
	"fmt"
	"net"
)

// BannedConnectionError bans a connection.
func BannedConnectionError(c net.Conn, cause error) error {
	return bannedConnection{
		conn:  c,
		cause: cause,
	}
}

type bannedConnection struct {
	conn  net.Conn
	cause error
}

func (t bannedConnection) Unwrap() error {
	return t.cause
}

func (t bannedConnection) Error() string {
	return fmt.Sprintf("banned connection %s: %s", t.conn.RemoteAddr().String(), t.cause)
}
