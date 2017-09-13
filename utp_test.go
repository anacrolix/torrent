package torrent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewUtpSocketErrorNilInterface(t *testing.T) {
	s, err := NewUtpSocket("fix", "your:language")
	assert.Error(t, err)
	if s != nil {
		t.Fatalf("expected nil, got %#v", s)
	}
}
