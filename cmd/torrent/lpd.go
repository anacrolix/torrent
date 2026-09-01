// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Portions derived from https://gitlab.com/axet/libtorrent/-/blob/master/lpd.go
// by Alexey Kuznetsov <axet@me.com>, relicensed under MPL-2.0 with the
// author's permission.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/bargle/v2"
)

const (
	lpdHost4        = "239.192.152.143:6771"
	lpdHost6        = "[ff15::efc0:988f]:6771"
	lpdAnnounceMsg  = "BT-SEARCH * HTTP/1.1\r\nHost: %s\r\nPort: %s\r\n%s\r\n\r\n"
	lpdInfohashLine = "Infohash: %s\r\n"
)

func lpdCmd(p *bargle.Parser) action {
	return parseSubcommand(p,
		subcommand{
			name: "listen",
			desc: "join the LPD multicast group and print incoming BT-SEARCH announcements",
			parse: func(p *bargle.Parser) action {
				var ip6 bool
				bargle.ParseAll(p,
					desc("also listen on the IPv6 multicast group", boolFlag("ip6", &ip6)),
				)
				return func() error {
					return lpdListen(ip6)
				}
			},
		},
		subcommand{
			name: "announce",
			desc: "send a BT-SEARCH announcement for the given infohashes and exit",
			parse: func(p *bargle.Parser) action {
				var args struct {
					Port       int
					Ip6        bool
					Infohashes []string
				}
				bargle.ParseAll(p,
					desc("BitTorrent listen port to include in the announcement",
						builtinLong("port", &args.Port)),
					desc("also send to the IPv6 multicast group", boolFlag("ip6", &args.Ip6)),
					desc("infohash hex strings to announce",
						positionals("infohashes", &args.Infohashes, bargle.BuiltinUnmarshaler[string])),
				)
				requireArg(p, "infohashes", len(args.Infohashes) != 0)
				return func() error {
					return lpdAnnounce(args.Port, args.Ip6, args.Infohashes)
				}
			},
		},
	)
}

func lpdListen(ip6 bool) error {
	type endpoint struct {
		network string
		host    string
	}
	endpoints := []endpoint{{"udp4", lpdHost4}}
	if ip6 {
		endpoints = append(endpoints, endpoint{"udp6", lpdHost6})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()

	type announcement struct {
		when time.Time
		src  string
		port string
		ihs  []string
	}

	ch := make(chan announcement, 16)
	errCh := make(chan error, len(endpoints))

	for _, ep := range endpoints {
		ep := ep
		addr, err := net.ResolveUDPAddr(ep.network, ep.host)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", ep.host, err)
		}
		conn, err := net.ListenMulticastUDP(ep.network, nil, addr)
		if err != nil {
			return fmt.Errorf("listening on %s: %w", ep.host, err)
		}
		defer conn.Close()
		go func() {
			<-ctx.Done()
			conn.Close()
		}()
		go func() {
			buf := make([]byte, 2000)
			for {
				n, from, err := conn.ReadFromUDP(buf)
				if err != nil {
					if ctx.Err() != nil {
						errCh <- nil
					} else {
						errCh <- err
					}
					return
				}
				req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf[:n])))
				if err != nil || req.Method != "BT-SEARCH" {
					continue
				}
				ihs := req.Header[http.CanonicalHeaderKey("Infohash")]
				port := req.Header.Get("Port")
				if len(ihs) == 0 || port == "" {
					continue
				}
				ch <- announcement{
					when: time.Now(),
					src:  from.IP.String(),
					port: port,
					ihs:  ihs,
				}
			}
		}()
	}

	hosts := lpdHost4
	if ip6 {
		hosts += " and " + lpdHost6
	}
	fmt.Fprintf(os.Stderr, "Listening on %s  (Ctrl-C to stop)\n", hosts)

	for {
		select {
		case a := <-ch:
			for _, ih := range a.ihs {
				fmt.Printf("%s  src=%-15s  port=%-5s  infohash=%s\n",
					a.when.Format("15:04:05.000"), a.src, a.port, strings.ToLower(ih))
			}
		case err := <-errCh:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func lpdAnnounce(port int, ip6 bool, infohashes []string) error {
	var ihLines string
	for _, ih := range infohashes {
		ihLines += fmt.Sprintf(lpdInfohashLine, strings.ToUpper(ih))
	}

	type target struct {
		network string
		host    string
	}
	targets := []target{{"udp4", lpdHost4}}
	if ip6 {
		targets = append(targets, target{"udp6", lpdHost6})
	}

	for _, t := range targets {
		packet := fmt.Sprintf(lpdAnnounceMsg, t.host, strconv.Itoa(port), ihLines)
		addr, err := net.ResolveUDPAddr(t.network, t.host)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", t.host, err)
		}
		conn, err := net.DialUDP(t.network, nil, addr)
		if err != nil {
			return fmt.Errorf("dialing %s: %w", t.host, err)
		}
		_, err = conn.Write([]byte(packet))
		conn.Close()
		if err != nil {
			return fmt.Errorf("sending to %s: %w", t.host, err)
		}
		fmt.Printf("announced %d infohash(es) to %s (port %d)\n", len(infohashes), t.host, port)
	}
	return nil
}
