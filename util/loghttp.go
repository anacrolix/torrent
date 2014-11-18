package util

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
)

func LoggedHTTPServe(addr string) {
	if addr == "" {
		addr = "localhost:6061"
	}
	netAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		log.Fatalf("error resolving http addr: %s", err)
	}
	var conn net.Listener
	for try := 0; try < 100; try++ {
		conn, err = net.ListenTCP("tcp", netAddr)
		if err == nil {
			break
		}
		netAddr.Port++
	}
	if err != nil {
		log.Fatalf("error creating http conn: %#v", err)
	}
	log.Printf("starting http server on http://%s", conn.Addr())
	go func() {
		defer conn.Close()
		err = (&http.Server{}).Serve(conn)
		if err != nil {
			log.Fatalf("error serving http: %s", err)
		}
	}()
}
