package mytimer

import (
	"time"
)

type TimeValue = time.Time

type Func = func() TimeValue
