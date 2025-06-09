package connections

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBloom(t *testing.T) {
	b := NewBloomBanIP(time.Second)

	require.NoError(t, b.Blocked(net.ParseIP("185.90.60.219"), 1))
	b.Inhibit(net.ParseIP("185.90.60.219"), 1, errors.New("cuz"))
	require.Error(t, b.Blocked(net.ParseIP("185.90.60.219"), 1))
}

func TestAutoFirewall(t *testing.T) {
	fw := AutoFirewall()

	require.NoError(t, fw.Blocked(net.ParseIP("185.90.60.219"), 1))
}
