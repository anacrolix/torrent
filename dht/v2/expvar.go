package dht

import (
	"expvar"
)

var (
	readZeroPort       = expvar.NewInt("dhtReadZeroPort")
	readBlocked        = expvar.NewInt("dhtReadBlocked")
	readNotKRPCDict    = expvar.NewInt("dhtReadNotKRPCDict")
	readUnmarshalError = expvar.NewInt("dhtReadUnmarshalError")
	readAnnouncePeer   = expvar.NewInt("dhtReadAnnouncePeer")
	announceErrors     = expvar.NewInt("dhtAnnounceErrors")
	writeErrors        = expvar.NewInt("dhtWriteErrors")
	writes             = expvar.NewInt("dhtWrites")
	expvars            = expvar.NewMap("dht")
)
