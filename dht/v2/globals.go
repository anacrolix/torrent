package dht

import (
	"github.com/anacrolix/stm/rate"
)

var defaultSendLimiter = rate.NewLimiter(25, 25)
