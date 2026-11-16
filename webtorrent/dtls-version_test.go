package webtorrent

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// pion/dtls/v3 before v3.0.11 (and v3.1.0) uses random AES-GCM nonces, which can leak the
// authentication key through nonce reuse: CVE-2026-26014 / GHSA-9f3f-wv7r-qc8r. See
// https://github.com/anacrolix/torrent/issues/1153. This guards against the dependency being
// downgraded back into the affected range.
func TestPionDtlsNotVulnerableToCve202626014(t *testing.T) {
	// Test binaries don't carry dependency build info, so check the module's go.mod, which lists
	// the selected version of every dependency.
	b, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	const path = "github.com/pion/dtls/v3"
	m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(path) + `\s+(\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%v not found in go.mod", path)
	}
	version := string(m[1])
	major, minor, patch, ok := parseSemver(version)
	if !ok || major != 3 {
		t.Fatalf("can't parse %v version %q", path, version)
	}
	if (minor == 0 && patch < 11) || (minor == 1 && patch < 1) {
		t.Fatalf("%v %v is affected by CVE-2026-26014, need >= v3.0.11 or >= v3.1.1", path, version)
	}
	if regexp.MustCompile(`(?m)^\s*(replace\s+)?` + regexp.QuoteMeta(path) + `\s.*=>`).Match(b) {
		t.Fatalf("%v is replaced in go.mod, check the replacement isn't affected", path)
	}
}

func parseSemver(v string) (major, minor, patch int, ok bool) {
	v, ok = strings.CutPrefix(v, "v")
	if !ok {
		return
	}
	// Drop any pre-release or build metadata.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}
