package errorsx_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/stretchr/testify/require"
)

func TestWithStackFormatting(t *testing.T) {
	require.Equal(t, "derp", fmt.Sprintf("%s", errorsx.WithStack(fmt.Errorf("derp"))))
	require.Equal(t, "derp", fmt.Sprintf("%s", errorsx.WithStack(errorsx.New("derp"))))
	require.Equal(t, "derp: 5", fmt.Sprintf("%s", errorsx.WithStack(errorsx.Errorf("derp: %d", 5))))
	require.Equal(t, "failed: derp", fmt.Sprintf("%s", errorsx.Wrap(fmt.Errorf("derp"), "failed")))

	require.Equal(t, "derp", fmt.Sprintf("%v", errorsx.WithStack(fmt.Errorf("derp"))))
	require.Equal(t, "derp", fmt.Sprintf("%v", errorsx.WithStack(errorsx.New("derp"))))
	require.Equal(t, "derp: 5", fmt.Sprintf("%v", errorsx.WithStack(errorsx.Errorf("derp: %d", 5))))
	require.Equal(t, "failed: derp", fmt.Sprintf("%v", errorsx.Wrap(fmt.Errorf("derp"), "failed")))

	require.Equal(t, "\"derp\"", fmt.Sprintf("%q", errorsx.WithStack(fmt.Errorf("derp"))))
	require.Equal(t, "\"derp\"", fmt.Sprintf("%q", errorsx.WithStack(errorsx.New("derp"))))
	require.Equal(t, "\"derp: 5\"", fmt.Sprintf("%q", errorsx.WithStack(errorsx.Errorf("derp: %d", 5))))
	require.Equal(t, "\"failed: derp\"", fmt.Sprintf("%q", errorsx.Wrap(fmt.Errorf("derp"), "failed")))
}

func TestTimedout(t *testing.T) {
	var (
		timedout errorsx.Timeout
		err      = errorsx.Timedout(errorsx.String("timeout"), time.Minute)
	)

	require.True(t, errors.As(err, &timedout))
	require.True(t, errorsx.Is(err, timedout))
}
