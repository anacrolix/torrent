package bytesx

// base 2 byte units
const (
	_   = iota
	KiB = 1 << (10 * iota)
	MiB
	GiB
	TiB
	PiB
	EiB
)
