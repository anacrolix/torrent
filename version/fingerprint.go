// based on libtorrent/src/fingerprint.cpp

package version

import (
	"fmt"
)

// versionToChar converts an integer version number (0–35) to a character.
// 0–9 → '0'–'9', 10+ → 'A', 'B', ...
func versionToChar(v int) rune {
	switch {
	case v >= 0 && v < 10:
		return rune('0' + v)
	case v >= 10:
		return rune('A' + (v - 10))
	default:
		panic("invalid version number for fingerprint")
	}
}

// GenerateFingerprint builds a 8-character fingerprint string from client info.
//
// Example: GenerateFingerprint("LT", 2, 1, 0, 0) → "-LT2100-"
func GenerateFingerprint(name string, major, minor, revision, tag int) string {
	if len(name) < 2 {
		name = "--"
	}

	if major < 0 || minor < 0 || revision < 0 || tag < 0 {
		panic("negative version number in fingerprint")
	}

	runes := []rune(name)
	return fmt.Sprintf("-%c%c%c%c%c%c-",
		runes[0],
		runes[1],
		versionToChar(major),
		versionToChar(minor),
		versionToChar(revision),
		versionToChar(tag),
	)
}
