package main

import (
	"context"
	"errors"
	"github.com/anacrolix/bargle/v2"
	"github.com/anacrolix/dht/v2"
	g "github.com/anacrolix/generics"
	app "github.com/anacrolix/gostdapp"
	"github.com/davecgh/go-spew/spew"
	"net"
)

func main() {
	app.RunContext(mainErr)
}

func mainErr(ctx context.Context) (err error) {
	s, err := dht.NewServer(nil)
	if err != nil {
		return
	}
	defer s.Close()
	serverNetwork := "udp"
	parser := bargle.NewParser()
	switch {
	case parser.Parse(bargle.Keyword("query")):
		var addr string
		var q g.Option[string]
		input := dht.QueryInput{}
		bargle.ParseAll(
			parser,
			bargle.Positional("addr", bargle.BuiltinUnmarshaler(&addr)),
			bargle.Positional("q", bargle.BuiltinOptionUnmarshaler(&q)),
			bargle.Long("target", bargle.TextUnmarshaler(&input.MsgArgs.Target)),
			bargle.Long("info-hash", bargle.TextUnmarshaler(&input.MsgArgs.InfoHash)),
		)
		parser.FailIfArgsRemain()
		if !parser.Ok() {
			break
		}
		if addr == "" {
			return errors.New("addr not specified")
		}
		if !q.Ok {
			return errors.New("q not specified")
		}
		udpAddr, err := net.ResolveUDPAddr(serverNetwork, addr)
		if err != nil {
			return err
		}
		res := s.Query(ctx, dht.NewAddr(udpAddr), q.Unwrap(), input)
		spew.Dump(res)
	default:
		parser.Fail()
	}
	parser.DoHelpIfHelping()
	return parser.Err()
}
