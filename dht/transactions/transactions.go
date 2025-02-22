package transactions

// Matches both ID and addr to transactions.
type Key struct {
	// The KRPC transaction ID.
	T Id
	// host:port
	RemoteAddr string
}

// Transaction key type, probably should match whatever is used in KRPC messages for the `t` field.
type Id = string
