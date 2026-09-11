package metainfo

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
)

// A .torrent whose info value is an extremely deeply nested list must decode to an error instead
// of recursing unboundedly and crashing the process with a stack overflow (the info value is
// captured via the bencode.Bytes unmarshaler, which drives the decoder's raw readOneValue path).
func TestLoadRejectsDeeplyNestedInfo(t *testing.T) {
	const depth = 10_000 // far beyond the decoder's default nesting limit; instant to build
	payload := "d4:info" + strings.Repeat("l", depth) + "i0e" + strings.Repeat("e", depth) + "ee"
	_, err := Load(bytes.NewBufferString(payload))
	var se *bencode.SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("expected bencode.SyntaxError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A v2 info dict whose file tree is nested deeper than the decoder's limit must be rejected up
// front with an error, instead of triggering the quadratic re-parsing in
// FileTree.UnmarshalBencode (each level re-runs bencode.Unmarshal over its whole subtree).
func TestUnmarshalInfoRejectsDeepFileTree(t *testing.T) {
	leaf := map[string]interface{}{
		"": map[string]interface{}{
			"length":      int64(5),
			"pieces root": string(bytes.Repeat([]byte{'A'}, 32)),
		},
	}
	node := leaf
	for i := 0; i < 100; i++ {
		node = map[string]interface{}{"a": node}
	}
	src, err := bencode.Marshal(map[string]interface{}{
		"meta version": int64(2),
		"piece length": int64(32768),
		"file tree":    node,
	})
	if err != nil {
		t.Fatal(err)
	}
	var info Info
	err = bencode.Unmarshal(src, &info)
	var se *bencode.SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("expected bencode.SyntaxError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Fatalf("unexpected error: %v", err)
	}
}
