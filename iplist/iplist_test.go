package iplist

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

var sample = `
# List distributed by iblocklist.com

a:1.2.4.0-1.2.4.255
b:1.2.8.0-1.2.8.255`

func sampleRanges(tb testing.TB) (ranges []Range, err error) {
	scanner := bufio.NewScanner(strings.NewReader(sample))
	for scanner.Scan() {
		r, ok, _ := ParseBlocklistP2PLine(scanner.Bytes())
		if ok {
			// tb.Log(r)
			ranges = append(ranges, r)
		}
	}
	err = scanner.Err()
	return
}

func BenchmarkParseP2pBlocklist(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sampleRanges(b)
	}
}

func TestSimple(t *testing.T) {
	ranges, err := sampleRanges(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 {
		t.Fatalf("expected 2 ranges but got %d", len(ranges))
	}
	iplist := New(ranges)
	for _, _case := range []struct {
		IP   string
		Hit  bool
		Desc string
	}{
		{"1.2.3.255", false, ""},
		{"1.2.8.0", true, "b"},
		{"1.2.4.255", true, "a"},
		// Try to roll over to the next octet on the parse.
		{"1.2.7.256", false, ""},
		{"1.2.8.254", true, "b"},
	} {
		r := iplist.Lookup(net.ParseIP(_case.IP))
		if !_case.Hit {
			if r != nil {
				t.Fatalf("got hit when none was expected")
			}
			continue
		}
		if r == nil {
			t.Fatalf("expected hit for %q", _case.IP)
		}
		if r.Description != _case.Desc {
			t.Fatalf("%q != %q", r.Description, _case.Desc)
		}
	}
}
