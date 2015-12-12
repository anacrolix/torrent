package dht

import "errors"

var (
	ErrNoStoreOnInsecureNodes = errors.New("Cannot store on node; either ID is not known, or is insecure.")
)
