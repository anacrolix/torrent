package util

import (
	"log"
	"net"
	"net/http"
)

func LoggedHTTPServe(addr string) {
	netAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		log.Fatalf("error resolving http addr: %s", err)
	}
	conn, err := net.ListenTCP("tcp", netAddr)
	if err != nil {
		log.Fatalf("error creating http conn: %s", err)
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
