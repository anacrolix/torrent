package bep0006_test

import (
	"net/netip"
	"testing"

	"github.com/james-lawrence/torrent/bep0006"
	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/stretchr/testify/require"
)

func TestAllowedFastSetGeneration(t *testing.T) {
	t.Run("Valid Inputs - Standard Case", func(t *testing.T) {
		infohash := int160.FromByteString("someinfodigest123456")
		ip1 := errorsx.Must(netip.ParseAddr("192.168.1.1"))
		bitmap, err := bep0006.AllowedFastSet(ip1, int160.ByteArray(infohash), 100, 10)
		require.NoError(t, err, "Expected no error for valid inputs")
		require.NotNil(t, bitmap, "Expected a non-nil bitmap")
		require.Greater(t, int(bitmap.GetCardinality()), 0, "Expected some bits to be set")
		require.LessOrEqual(t, int(bitmap.GetCardinality()), 10, "Expected cardinality less than or equal to k")
	})

	t.Run("Valid Inputs - IPv6", func(t *testing.T) {
		ip := errorsx.Must(netip.ParseAddr("2001:0db8:85a3:0000:0000:8a2e:0370:7334"))
		infohash := int160.FromByteString("someinfodigest123456")
		bitmap, err := bep0006.AllowedFastSet(ip, int160.ByteArray(infohash), 100, 10)
		require.NoError(t, err, "Expected no error for valid inputs")
		require.NotNil(t, bitmap, "Expected a non-nil bitmap")
		require.Greater(t, int(bitmap.GetCardinality()), 0, "Expected some bits to be set")
		require.LessOrEqual(t, int(bitmap.GetCardinality()), 10, "Expected cardinality less than or equal to k")
	})

	t.Run("Valid Inputs - Edge Case numPieces equals k", func(t *testing.T) {
		infohash := int160.FromByteString("anotherdigest7890123")
		ip2 := errorsx.Must(netip.ParseAddr("203.0.113.42"))
		bitmap, err := bep0006.AllowedFastSet(ip2, infohash.AsByteArray(), 5, 5)
		require.NoError(t, err, "Expected no error when numPieces equals k")
		require.NotNil(t, bitmap, "Expected a non-nil bitmap")
		require.Greater(t, int(bitmap.GetCardinality()), 0, "Expected some bits to be set")
		require.LessOrEqual(t, int(bitmap.GetCardinality()), 5, "Expected cardinality less than or equal to k")
	})

	t.Run("Error Case - numPieces is zero", func(t *testing.T) {
		infohash := int160.FromByteString("someinfodigest123456")
		ip1 := errorsx.Must(netip.ParseAddr("192.168.1.1"))
		bitmap, err := bep0006.AllowedFastSet(ip1, infohash.AsByteArray(), 0, 5)
		require.Error(t, err, "Expected an error when numPieces is zero")
		require.Contains(t, err.Error(), "numPieces cannot be zero", "Error message mismatch")
		require.Nil(t, bitmap, "Expected a nil bitmap on error")
	})

	t.Run("Error Case - k is zero", func(t *testing.T) {
		infohash := int160.FromByteString("someinfodigest123456")
		ip1 := errorsx.Must(netip.ParseAddr("192.168.1.1"))
		bitmap, err := bep0006.AllowedFastSet(ip1, infohash.AsByteArray(), 100, 0)
		require.NoError(t, err)
		require.Zero(t, bitmap.GetCardinality(), "Expected a nil bitmap on error")
	})

	t.Run("Error Case - k greater than numPieces", func(t *testing.T) {
		infohash := int160.FromByteString("anotherdigest7890123")
		ip2 := errorsx.Must(netip.ParseAddr("203.0.113.42"))
		bitmap, err := bep0006.AllowedFastSet(ip2, infohash.AsByteArray(), 10, 15)
		require.Error(t, err, "Expected an error when k is greater than numPieces")
		require.Contains(t, err.Error(), "k cannot be greater than numPieces", "Error message mismatch")
		require.Nil(t, bitmap, "Expected a nil bitmap on error")
	})

	t.Run("Different IP and InfoHash", func(t *testing.T) {
		infohash1 := int160.FromByteString("someinfodigest123456")
		ip1 := errorsx.Must(netip.ParseAddr("192.168.1.1"))
		infohash2 := int160.FromByteString("anotherdigest7890123")
		ip2 := errorsx.Must(netip.ParseAddr("203.0.113.42"))
		bitmap1, err := bep0006.AllowedFastSet(ip1, infohash1.AsByteArray(), 50, 5)
		require.NoError(t, err)
		bitmap2, err := bep0006.AllowedFastSet(ip2, infohash2.AsByteArray(), 50, 5)
		require.NoError(t, err)

		require.False(t, bitmap1.Equals(bitmap2), "bitmaps should not be equal for different ip")
	})
}
