package metainfo

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestParseMagnetV2(t *testing.T) {
	c := qt.New(t)

	const v2Only = "magnet:?xt=urn:btmh:1220caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e&dn=bittorrent-v2-test"

	m2, err := ParseMagnetV2Uri(v2Only)
	c.Assert(err, qt.IsNil)
	c.Check(m2.InfoHash.Ok, qt.IsFalse)
	c.Check(m2.V2InfoHash.Ok, qt.IsTrue)
	c.Check(m2.V2InfoHash.Value.HexString(), qt.Equals, "caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e")
	c.Check(m2.Params, qt.HasLen, 0)

	_, err = ParseMagnetUri(v2Only)
	c.Check(err, qt.IsNotNil)

	const hybrid = "magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac&xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb&dn=bittorrent-v1-v2-hybrid-test"

	m2, err = ParseMagnetV2Uri(hybrid)
	c.Assert(err, qt.IsNil)
	c.Check(m2.InfoHash.Ok, qt.IsTrue)
	c.Check(m2.InfoHash.Value.HexString(), qt.Equals, "631a31dd0a46257d5078c0dee4e66e26f73e42ac")
	c.Check(m2.V2InfoHash.Ok, qt.IsTrue)
	c.Check(m2.V2InfoHash.Value.HexString(), qt.Equals, "d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb")
	c.Check(m2.Params, qt.HasLen, 0)

	m, err := ParseMagnetUri(hybrid)
	c.Assert(err, qt.IsNil)
	c.Check(m.InfoHash.HexString(), qt.Equals, "631a31dd0a46257d5078c0dee4e66e26f73e42ac")
	c.Check(m.Params["xt"], qt.HasLen, 1)
}
