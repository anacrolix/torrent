package torrent

import (
	"testing"

	"github.com/anacrolix/log"
	"github.com/stretchr/testify/assert"
)

func TestNewUtpSocketErrorNilInterface(t *testing.T) {
	s, err := NewUtpSocket("fix", "your:language", nil, log.Default)
	assert.Error(t, err)
	if s != nil {
		t.Fatalf("expected nil, got %#v", s)
	}
}
