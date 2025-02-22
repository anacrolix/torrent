package main

import (
	"context"
	stdLog "log"
	"net"
	"net/http"
	"os"
	"os/signal"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/dht/v2"
)

var (
	flags = struct {
		TableFile   string `help:"name of file for storing node info"`
		Addr        string `help:"local UDP address"`
		NoBootstrap bool
	}{
		Addr: ":0",
	}
	s *dht.Server
)

func loadTable() (err error) {
	added, err := s.AddNodesFromFile(flags.TableFile)
	log.Printf("loaded %d nodes from table file", added)
	return
}

func saveTable() error {
	return dht.WriteNodesToFile(s.Nodes(), flags.TableFile)
}

func main() {
	stdLog.SetFlags(stdLog.LstdFlags | stdLog.Lshortfile)
	err := mainErr()
	if err != nil {
		log.Printf("error in main: %v", err)
		os.Exit(1)
	}
}

func mainErr() error {
	tagflag.Parse(&flags)
	conn, err := net.ListenPacket("udp", flags.Addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = conn
	cfg.Logger = log.Default.FilterLevel(log.Info)
	cfg.NoSecurity = false
	s, err = dht.NewServer(cfg)
	if err != nil {
		return err
	}
	http.HandleFunc("/debug/dht", func(w http.ResponseWriter, r *http.Request) {
		s.WriteStatus(w)
	})
	if flags.TableFile != "" {
		err = loadTable()
		if err != nil {
			return err
		}
	}
	log.Printf("dht server on %s, ID is %x", s.Addr(), s.ID())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		log.Printf("got signal: %v", <-ch)
		cancel()
	}()
	if !flags.NoBootstrap {
		go func() {
			if tried, err := s.Bootstrap(); err != nil {
				log.Printf("error bootstrapping: %s", err)
			} else {
				log.Printf("finished bootstrapping: %#v", tried)
			}
		}()
	}
	<-ctx.Done()
	s.Close()

	if flags.TableFile != "" {
		if err := saveTable(); err != nil {
			log.Printf("error saving node table: %s", err)
		}
	}
	return nil
}
