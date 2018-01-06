package torrent

import (
	"fmt"
	"testing"

	"github.com/anacrolix/missinggo"
	"github.com/stretchr/testify/assert"
)

func TestPeerIdString(t *testing.T) {
	for _, _case := range []struct {
		id string
		s  string
	}{
		{"\x1cNJ}\x9c\xc7\xc4o\x94<\x9b\x8c\xc2!I\x1c\a\xec\x98n", "1c4e4a7d9cc7c46f943c9b8cc221491c07ec986e"},
		{"-FD51W\xe4-LaZMk0N8ZLA7", "-FD51W\xe4-4c615a4d6b304e385a4c4137"},
	} {
		var pi PeerID
		missinggo.CopyExact(&pi, _case.id)
		assert.EqualValues(t, _case.s, pi.String())
		assert.EqualValues(t, fmt.Sprintf("%q", _case.s), fmt.Sprintf("%q", pi))
	}
}
