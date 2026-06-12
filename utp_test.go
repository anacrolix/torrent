package torrent

import (
	"testing"

	"github.com/anacrolix/log"
	qt "github.com/go-quicktest/qt"
)

func TestNewUtpSocketErrorNilInterface(t *testing.T) {
	s, err := NewUtpSocket("fix", "your:language", nil, log.Default)
	qt.Check(t, qt.IsNotNil(err))
	if s != nil {
		t.Fatalf("expected nil, got %#v", s)
	}
}
