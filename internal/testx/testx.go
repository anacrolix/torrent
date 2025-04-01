package testx

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/stretchr/testify/require"
)

func Context(t testing.TB) (context.Context, context.CancelFunc) {
	return context.WithCancel(t.Context())
}

// Must is a small language extension for panicing on the common
// value, error return pattern. only used in tests.
func Must[T any](v T, err error) func(t testing.TB) T {
	return func(t testing.TB) T {
		require.NoError(t, err)
		return v
	}
}

func ReadMD5(path ...string) string {
	d := md5.New()
	_ = errorsx.Must(d.Write(errorsx.Must(os.ReadFile(filepath.Join(path...)))))
	return hex.EncodeToString(d.Sum(nil))
}
