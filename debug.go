package torrent

import (
	"context"
	"log"
	"os"
	"syscall"

	"github.com/anacrolix/torrent/internal/x/debugx"
)

func init() {
	log.Println("PID", os.Getpid())
	go debugx.DumpOnSignal(context.Background(), syscall.SIGUSR2)
}
