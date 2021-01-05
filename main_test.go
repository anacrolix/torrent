package torrent

import (
	"context"
	"log"
	"os"
	"syscall"
	"testing"

	"github.com/james-lawrence/torrent/internal/x/debugx"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func TestMain(m *testing.M) {
	go debugx.DumpOnSignal(context.Background(), syscall.SIGUSR2)
	code := func() int {
		// defer profile.Start(profile.CPUProfile, profile.ProfilePath(".")).Stop()
		// defer profile.Start(profile.BlockProfile, profile.ProfilePath(".")).Stop()
		// defer profile.Start(profile.MutexProfile, profile.ProfilePath(".")).Stop()
		// defer profile.Start(profile.TraceProfile, profile.ProfilePath(".")).Stop()
		return m.Run()
	}()

	os.Exit(code)
}
