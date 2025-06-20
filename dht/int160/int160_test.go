package int160_test

import (
	"crypto/rand"
	"testing"

	"github.com/james-lawrence/torrent/dht/int160"

	"github.com/stretchr/testify/require"
)

func TestInt160(t *testing.T) {
	t.Run("RandomPrefixed", func(t *testing.T) {
		t.Run("should have provided prefix", func(t *testing.T) {
			v, err := int160.RandomPrefixed("EX")
			require.NoError(t, err)
			require.Equal(t, "EX", string(v.Bytes()[:2]))
		})

		t.Run("have identically to original impl", func(t *testing.T) {
			var (
				pid [20]byte
			)
			o := copy(pid[:], "EX")
			n, err := rand.Read(pid[o:])
			require.NoError(t, err)
			require.Equal(t, 18, n)

			v, err := int160.RandomPrefixed("EX")
			require.NoError(t, err)
			require.Equal(t, "EX", v.ByteString()[:2])
			require.Equal(t, pid[:2], v.Bytes()[:2])
		})
	})

	t.Run("cmp", func(t *testing.T) {
		t.Run("identical should result in 0", func(t *testing.T) {
			i1 := int160.New("abc")
			i2 := int160.New("abc")

			require.Equal(t, 0, i1.Cmp(i2))
		})

		t.Run("i1 < i2 should be -1", func(t *testing.T) {
			i1 := int160.New("abc0")
			i2 := int160.New("abc1")

			require.Equal(t, -1, i1.Cmp(i2))
		})

		t.Run("i1 > i2 should be 1", func(t *testing.T) {
			i1 := int160.New("abc1")
			i2 := int160.New("abc0")

			require.Equal(t, 1, i1.Cmp(i2))
		})
	})
}
