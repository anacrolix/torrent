package iplist

import (
	"bufio"
	"io"
	"net"
)

func ParseCIDRListReader(r io.Reader) (ret []Range, err error) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		err = func() (err error) {
			_, in, err := net.ParseCIDR(s.Text())
			if err != nil {
				return
			}
			ret = append(ret, Range{
				First: in.IP,
				Last:  IPNetLast(in),
			})
			return
		}()
		if err != nil {
			return
		}
	}
	return
}

// Returns the last, inclusive IP in a net.IPNet.
func IPNetLast(in *net.IPNet) (last net.IP) {
    last = make(net.IP, len(in.IP))
	for i := 0; i < len(last) && i < len(in.IP); i++ {
        if len(in.IP) != len(in.Mask) {
            panic("wat")
        }
		last[i] = in.IP[i] | ^in.Mask[i]
	}
	return
}
