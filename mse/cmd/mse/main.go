package main

import (
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/alexflint/go-arg"
	"golang.org/x/xerrors"

	"github.com/anacrolix/torrent/mse"
)

func main() {
	err := mainErr()
	if err != nil {
		log.Fatalf("fatal error: %v", err)
	}
}

func mainErr() error {
	var args = struct {
		CryptoMethod mse.CryptoMethod
		Dial         *struct {
			Network        string `arg:"positional"`
			Address        string `arg:"positional"`
			SecretKey      string `arg:"positional"`
			InitialPayload []byte
		} `arg:"subcommand""`
		Listen *struct {
			Network    string   `arg:"positional"`
			Address    string   `arg:"positional"`
			SecretKeys []string `arg:"positional"`
		} `arg:"subcommand""`
	}{
		CryptoMethod: mse.AllSupportedCrypto,
	}
	arg.MustParse(&args)
	if args.Dial != nil {
		cn, err := net.Dial(args.Dial.Network, args.Dial.Address)
		if err != nil {
			return xerrors.Errorf("dialing: %w", err)
		}
		defer cn.Close()
		rw, _, err := mse.InitiateHandshake(cn, []byte(args.Dial.SecretKey), args.Dial.InitialPayload, args.CryptoMethod)
		if err != nil {
			return xerrors.Errorf("initiating handshake: %w", err)
		}
		doStreaming(rw)
	}
	if args.Listen != nil {
		l, err := net.Listen(args.Listen.Network, args.Listen.Address)
		if err != nil {
			return xerrors.Errorf("listening: %w", err)
		}
		defer l.Close()
		cn, err := l.Accept()
		l.Close()
		if err != nil {
			return xerrors.Errorf("accepting: %w", err)
		}
		defer cn.Close()
		rw, _, err := mse.ReceiveHandshake(cn, func(f func([]byte) bool) {
			for _, sk := range args.Listen.SecretKeys {
				f([]byte(sk))
			}
		}, mse.DefaultCryptoSelector)
		if err != nil {
			log.Fatalf("error receiving: %v", err)
		}
		doStreaming(rw)
	}
	return nil
}

func doStreaming(rw io.ReadWriter) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Println(io.Copy(rw, os.Stdin))
	}()
	go func() {
		defer wg.Done()
		log.Println(io.Copy(os.Stdout, rw))
	}()
	wg.Wait()
}
