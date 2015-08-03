package dht

import (
	"expvar"
)

var (
	read               = expvar.NewInt("dhtRead")
	readBlocked        = expvar.NewInt("dhtReadBlocked")
	readUnmarshalError = expvar.NewInt("dhtReadUnmarshalError")
	readQuery          = expvar.NewInt("dhtReadQuery")
	announceErrors     = expvar.NewInt("dhtAnnounceErrors")
)
