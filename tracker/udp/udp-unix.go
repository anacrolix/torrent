//go:build !windows

package udp

import (
	"strings"
)

func isErrMessageTooLong(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "message too long")
}
