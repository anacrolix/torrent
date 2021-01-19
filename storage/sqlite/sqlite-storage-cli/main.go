package main

import (
	"fmt"
	"log"
	"os"

	"crawshaw.io/sqlite"
	"github.com/alexflint/go-arg"
	sqliteStorage "github.com/anacrolix/torrent/storage/sqlite"
)

type InitCommand struct {
	Path string `arg:"positional"`
}

func main() {
	err := mainErr()
	if err != nil {
		log.Printf("error in main: %v", err)
		os.Exit(1)
	}
}

func mainErr() error {
	var args struct {
		Init *InitCommand `arg:"subcommand"`
	}
	p := arg.MustParse(&args)
	switch {
	case args.Init != nil:
		conn, err := sqlite.OpenConn(args.Init.Path, 0)
		if err != nil {
			return fmt.Errorf("opening sqlite conn: %w", err)
		}
		defer conn.Close()
		return sqliteStorage.InitSchema(conn, 1<<14, true)
	default:
		p.Fail("expected subcommand")
		panic("unreachable")
	}
}
