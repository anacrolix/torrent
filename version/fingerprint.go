// based on libtorrent/src/fingerprint.cpp

package version

// versionToChar converts an integer version number (0–35) to an ASCII byte.
// 0–9 → '0'–'9'
// 10-35 → 'A'-'Z'
func versionToChar(v int) byte {
	switch {
	case 0 <= v && v < 10:
		return byte('0' + v)
	case 10 <= v && v < 36:
		return byte('A' + (v - 10))
	default:
		panic("invalid version number for fingerprint")
	}
}

// GenerateFingerprint builds an 8-byte fingerprint string from client info.
//
// Example: GenerateFingerprint("LT", 2, 1, 0, 0) → "-LT2100-"
func GenerateFingerprint(name string, major, minor, revision, tag int) string {
	// ensure name[0] and name[1] are always defined
	switch len(name) {
	case 0:
		name = "--"
	case 1:
		name += "-"
	}

	if major < 0 || minor < 0 || revision < 0 || tag < 0 {
		panic("negative version number in fingerprint")
	}

	return string([]byte{
		'-',
		name[0],
		name[1],
		versionToChar(major),
		versionToChar(minor),
		versionToChar(revision),
		versionToChar(tag),
		'-',
	})
}
