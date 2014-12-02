package iplist

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

type IPList struct {
	ranges []Range
}

type Range struct {
	First, Last net.IP
	Description string
}

func (r *Range) String() string {
	return fmt.Sprintf("%s-%s (%s)", r.First, r.Last, r.Description)
}

// Create a new IP list. The given range must already sorted by the lower IP
// in the range. Behaviour is undefined for lists of overlapping ranges.
func New(initSorted []Range) *IPList {
	return &IPList{
		ranges: initSorted,
	}
}

// Return the range the given IP is in. Returns nil if no range is found.
func (me *IPList) Lookup(ip net.IP) (r *Range) {
	// Find the index of the first range for which the following range exceeds
	// it.
	i := sort.Search(len(me.ranges), func(i int) bool {
		if i+1 >= len(me.ranges) {
			return true
		}
		return bytes.Compare(ip, me.ranges[i+1].First) < 0
	})
	if i == len(me.ranges) {
		return
	}
	r = &me.ranges[i]
	if bytes.Compare(ip, r.First) < 0 || bytes.Compare(ip, r.Last) > 0 {
		r = nil
	}
	return
}

var p2pBlocklistLineRe = regexp.MustCompile(`(.*):([\d.]+)-([\d.]+)`)

// Parse a line of the PeerGuardian Text Lists (P2P) Format. Returns !ok but
// no error if a line doesn't contain a range but isn't erroneous, such as
// comment and blank lines.
func ParseBlocklistP2PLine(l string) (r Range, ok bool, err error) {
	l = strings.TrimSpace(l)
	if l == "" || strings.HasPrefix(l, "#") {
		return
	}
	sms := p2pBlocklistLineRe.FindStringSubmatch(l)
	if sms == nil {
		err = fmt.Errorf("error parsing %q", l)
		return
	}
	r.Description = sms[1]
	r.First = net.ParseIP(sms[2])
	r.Last = net.ParseIP(sms[3])
	if r.First == nil || r.Last == nil {
		return
	}
	ok = true
	return
}
