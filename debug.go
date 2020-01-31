package torrent

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

func init() {
	log.Println("PID", os.Getpid())
	go DumpOnSignal(context.Background(), syscall.SIGUSR2)
}

// Compact returns the first error in the set, if any.
func Compact(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func genDst() (path string, dst io.WriteCloser) {
	var (
		err error
	)

	t := time.Now()
	ts := Reverse(strconv.Itoa(int(t.Unix())))
	path = filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s-%s.trace", filepath.Base(os.Args[0]), t.Format("2006-01-02"), ts))

	if dst, err = os.Create(path); err != nil {
		log.Println(errors.Wrapf(err, "failed to open file: %s", path))
		log.Println("routine dump falling back to stderr")
		return "stderr", WriteNopCloser(os.Stderr)
	}

	return path, dst
}

// DumpRoutines writes current goroutine stack traces to a temp file.
// and returns that files path. if for some reason a file could not be opened
// it falls back to stderr
func DumpRoutines() (path string, err error) {
	var (
		dst io.WriteCloser
	)

	path, dst = genDst()
	return path, Compact(pprof.Lookup("goroutine").WriteTo(dst, 1), dst.Close())
}

// DumpOnSignal runs the DumpRoutes method and prints to stderr whenever one of the provided
// signals is received.
func DumpOnSignal(ctx context.Context, sigs ...os.Signal) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, sigs...)

	for {
		select {
		case <-ctx.Done():
			return
		case _ = <-signals:
			if path, err := DumpRoutines(); err == nil {
				log.Println("dump located at:", path)
			} else {
				log.Println("failed to dump routines:", err)
			}
		}
	}
}

type writeNopCloser struct {
	io.Writer
}

func (writeNopCloser) Close() error { return nil }

// WriteNopCloser returns a WriteCloser with a no-op Close method wrapping
// the provided Writer w.
func WriteNopCloser(w io.Writer) io.WriteCloser {
	return writeNopCloser{w}
}

// Reverse returns the string reversed rune-wise left to right.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < len(r)/2; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func trace(msg string) {
	if pc, _, _, ok := runtime.Caller(1); ok {
		details := runtime.FuncForPC(pc)
		log.Output(2, fmt.Sprintln(details.Name(), "-", msg))
	}
}

func newDebugLock(m *sync.RWMutex) *debugLock {
	return &debugLock{m: m}
}

type rwmutex interface {
	Lock()
	Unlock()
	RLock()
	RUnlock()
}

type debugLock struct {
	m      *sync.RWMutex
	lcount uint64
	ucount uint64
}

func (t *debugLock) Lock() {
	updated := atomic.AddUint64(&t.lcount, 1)
	log.Output(2, fmt.Sprintf("%p lock initiated - %d", t.m, updated))
	t.m.Lock()
	log.Output(2, fmt.Sprintf("%p lock completed - %d", t.m, updated))
}

func (t *debugLock) Unlock() {
	updated := atomic.AddUint64(&t.ucount, 1)
	log.Output(2, fmt.Sprintf("%p unlock initiated - %d", t.m, updated))
	t.m.Unlock()
	log.Output(2, fmt.Sprintf("%p unlock completed - %d", t.m, updated))
}

func (t *debugLock) RLock() {
	updated := atomic.AddUint64(&t.lcount, 1)
	log.Output(2, fmt.Sprintf("%p rlock initiated - %d", t.m, updated))
	t.m.RLock()
	log.Output(2, fmt.Sprintf("%p rlock completed - %d", t.m, updated))
}

func (t *debugLock) RUnlock() {
	updated := atomic.AddUint64(&t.ucount, 1)
	log.Output(2, fmt.Sprintf("%p runlock initiated - %d", t.m, updated))
	t.m.RUnlock()
	log.Output(2, fmt.Sprintf("%p runlock completed - %d", t.m, updated))
}
