package metainfo

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestParseMagnetV2(t *testing.T) {

	const v2Only = "magnet:?xt=urn:btmh:1220caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e&dn=bittorrent-v2-test"

	m2, err := ParseMagnetV2Uri(v2Only)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(m2.InfoHash.Ok))
	qt.Check(t, qt.IsTrue(m2.V2InfoHash.Ok))
	qt.Check(t, qt.Equals(m2.V2InfoHash.Value.HexString(), "caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e"))
	qt.Check(t, qt.HasLen(m2.Params, 0))

	_, err = ParseMagnetUri(v2Only)
	qt.Check(t, qt.IsNotNil(err))

	const hybrid = "magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac&xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb&dn=bittorrent-v1-v2-hybrid-test"

	m2, err = ParseMagnetV2Uri(hybrid)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(m2.InfoHash.Ok))
	qt.Check(t, qt.Equals(m2.InfoHash.Value.HexString(), "631a31dd0a46257d5078c0dee4e66e26f73e42ac"))
	qt.Check(t, qt.IsTrue(m2.V2InfoHash.Ok))
	qt.Check(t, qt.Equals(m2.V2InfoHash.Value.HexString(), "d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb"))
	qt.Check(t, qt.HasLen(m2.Params, 0))

	m, err := ParseMagnetUri(hybrid)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(m.InfoHash.HexString(), "631a31dd0a46257d5078c0dee4e66e26f73e42ac"))
	qt.Check(t, qt.HasLen(m.Params["xt"], 1))
}

func TestParseMagnetV2Errors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		uri     string
		wantErr string
	}{
		{
			name:    "missing xt with dn",
			uri:     "magnet:?dn=only-a-name",
			wantErr: "missing infohash",
		},
		{
			name:    "bare scheme",
			uri:     "magnet:",
			wantErr: "missing infohash",
		},
		{
			name:    "empty query",
			uri:     "magnet:?",
			wantErr: "missing infohash",
		},
		{
			name:    "empty xt",
			uri:     "magnet:?xt=",
			wantErr: "missing infohash",
		},
		{
			name:    "non-bittorrent urn",
			uri:     "magnet:?xt=urn:sha1:YNCKHTQCWBTRNJIV4WNAE52SJUQCZO5C",
			wantErr: "missing infohash",
		},
		{
			name: "empty btih",
			uri:  "magnet:?xt=urn:btih:",
		},
		{
			name: "empty btmh",
			uri:  "magnet:?xt=urn:btmh:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMagnetV2Uri(tc.uri)
			qt.Assert(t, qt.IsNotNil(err))
			if tc.wantErr != "" {
				qt.Check(t, qt.Equals(err.Error(), tc.wantErr))
			}
		})
	}
}

func TestParseMagnetV2ValidCases(t *testing.T) {
	for _, tc := range []struct {
		name      string
		uri       string
		wantV1    bool
		wantV1Hex string
		wantV2    bool
		wantV2Hex string
		wantXT    []string
	}{
		{
			name:      "v1 hex",
			uri:       "magnet:?xt=urn:btih:51340689c960f0778a4387aef9b4b52fd08390cd&dn=v1-hex",
			wantV1:    true,
			wantV1Hex: "51340689c960f0778a4387aef9b4b52fd08390cd",
		},
		{
			name:   "v1 base32",
			uri:    "magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU",
			wantV1: true,
		},
		{
			name:      "v2 only",
			uri:       "magnet:?xt=urn:btmh:1220caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e&dn=bittorrent-v2-test",
			wantV2:    true,
			wantV2Hex: "caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e",
		},
		{
			name:      "hybrid",
			uri:       "magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac&xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb&dn=bittorrent-v1-v2-hybrid-test",
			wantV1:    true,
			wantV1Hex: "631a31dd0a46257d5078c0dee4e66e26f73e42ac",
			wantV2:    true,
			wantV2Hex: "d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb",
		},
		{
			name:      "valid xt combined with unrecognized xt",
			uri:       "magnet:?xt=urn:sha1:YNCKHTQCWBTRNJIV4WNAE52SJUQCZO5C&xt=urn:btih:51340689c960f0778a4387aef9b4b52fd08390cd",
			wantV1:    true,
			wantV1Hex: "51340689c960f0778a4387aef9b4b52fd08390cd",
			wantXT:    []string{"urn:sha1:YNCKHTQCWBTRNJIV4WNAE52SJUQCZO5C"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseMagnetV2Uri(tc.uri)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(m.InfoHash.Ok, tc.wantV1))
			if tc.wantV1Hex != "" {
				qt.Check(t, qt.Equals(m.InfoHash.Value.HexString(), tc.wantV1Hex))
			} else if tc.wantV1 {
				m1, err := ParseMagnetUri(tc.uri)
				qt.Assert(t, qt.IsNil(err))
				qt.Check(t, qt.Equals(m.InfoHash.Value, m1.InfoHash))
			}
			qt.Check(t, qt.Equals(m.V2InfoHash.Ok, tc.wantV2))
			if tc.wantV2Hex != "" {
				qt.Check(t, qt.Equals(m.V2InfoHash.Value.HexString(), tc.wantV2Hex))
			}
			qt.Check(t, qt.DeepEquals(m.Params["xt"], tc.wantXT))
			mRoundTrip, err := ParseMagnetV2Uri(m.String())
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.DeepEquals(mRoundTrip, m))
		})
	}
}
