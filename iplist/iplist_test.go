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

func TestSimple(t *testing.T) {
	var ranges []Range
	scanner := bufio.NewScanner(strings.NewReader(sample))
	for scanner.Scan() {
		r, ok, _ := ParseBlocklistP2PLine(scanner.Text())
		if ok {
			t.Log(r)
			ranges = append(ranges, r)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("error while scanning: %s", err)
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
