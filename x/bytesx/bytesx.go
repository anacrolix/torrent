package bytesx

import (
	"fmt"
)

type Unit int64

func (t Unit) Format(f fmt.State, verb rune) {
	div := int64(1)
	suffix := ""
	switch {
	case t > EiB:
		div = EiB
		suffix = "e"
	case t > PiB:
		div = PiB
		suffix = "p"
	case t > TiB:
		div = TiB
		suffix = "t"
	case t > GiB:
		div = GiB
		suffix = "g"
	case t > MiB:
		div = MiB
		suffix = "m"
	case t > KiB:
		div = KiB
		suffix = "k"
	}

	f.Write([]byte(fmt.Sprintf("%d%s", uint64(float64(t)/float64(div)), suffix)))
}

// base 2 byte units
const (
	_   Unit = iota
	KiB      = 1 << (10 * iota)
	MiB
	GiB
	TiB
	PiB
	EiB
)
