package storage

import (
	"log"
	"path/filepath"
	"testing"
)

func init() {
	log.SetFlags(log.Flags() | log.Lshortfile)
}

func TestSafePath(t *testing.T) {
	for _, _case := range []struct {
		input     []string
		expected  string
		expectErr bool
	}{
		{input: []string{"a", filepath.FromSlash(`b/../../..`)}, expectErr: true},
		{input: []string{"a", filepath.FromSlash(`b/../.././..`)}, expectErr: true},
		{input: []string{
			filepath.FromSlash(`NewSuperHeroMovie-2019-English-720p.avi /../../../../../Roaming/Microsoft/Windows/Start Menu/Programs/Startup/test3.exe`)},
			expectErr: true,
		},
	} {
		actual, err := ToSafeFilePath(_case.input...)
		if _case.expectErr {
			if err != nil {
				continue
			}
			t.Errorf("%q: expected error, got output %q", _case.input, actual)
		}
	}
}
