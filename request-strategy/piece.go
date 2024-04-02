package requestStrategy

type Piece interface {
	// Whether requests should be made for this piece. This would be false for cases like the piece
	// is currently being hashed, or already complete.
	Request() bool
	// Whether the piece should be counted towards the unverified bytes limit. The intention is to
	// prevent pieces being starved from the opportunity to move to the completed state.
	CountUnverified() bool
}
