package utpx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewUTPSocketErrorNilInterface(t *testing.T) {
	s, err := New("fix", "your:language")
	assert.Error(t, err)
	if s != nil {
		t.Fatalf("expected nil, got %#v", s)
	}
}
