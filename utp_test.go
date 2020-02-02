package torrent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewUTPSocketErrorNilInterface(t *testing.T) {
	s, err := newUTPSocket("fix", "your:language", nil)
	assert.Error(t, err)
	if s != nil {
		t.Fatalf("expected nil, got %#v", s)
	}
}
