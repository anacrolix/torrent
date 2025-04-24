package torrent

import (
	"io"

	"github.com/davecgh/go-spew/spew"
)

var spewConfig = spew.NewDefaultConfig()

func init() {
	//spewConfig.DisablePointerAddresses = true
	//spewConfig.DisablePointerMethods = true
}

func dumpStats(w io.Writer, a ...any) {
	spewConfig.Fdump(w, a...)
}
