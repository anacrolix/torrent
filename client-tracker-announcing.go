package torrent

import (
	"context"
	"fmt"
	"iter"
	"net/url"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/tracker"
)

func (cl *Client) startRegularTrackerAnnouncer(clientKey clientTrackerKey) {
	if cl.config.DisableTrackers {
		return
	}
	announcer, ok := cl.regularTrackers[clientKey]
	if ok {
		// We're assuming that we just added a torrent and the client might be asleep with nothing
		// to do?
		announcer.sleepCond.Broadcast()
		return
	}
	switch clientKey.Network {
	case "udp4":
		if cl.config.DisableIPv4Peers || cl.config.DisableIPv4 {
			return
		}
	case "udp6":
		if cl.config.DisableIPv6 {
			return
		}
	case "ws", "wss":
		// TODO: Re-enable websocket trackers.
		return
	}
	urlStr := clientKey.Url
	trOpts := tracker.NewClientOpts{
		UdpNetwork: clientKey.Network,
	}
	tc, err := tracker.NewClient(urlStr, trOpts)
	if err != nil {
		err = fmt.Errorf("creating tracker client for %q: %w", urlStr, err)
	}
	panicif.Err(err)
	ctx, cancel := context.WithCancelCause(context.Background())
	announcer = &trackerAnnouncer{
		cancel: cancel,
		cl:     cl,
		tc:     tc,
		tUrl:   urlStr,
		tKey:   clientKey,
		logger: cl.slogger.With("trackerUrl", urlStr),
	}
	g.MakeMapIfNil(&cl.regularTrackers)
	g.MapMustAssignNew(cl.regularTrackers, clientKey, announcer)
	go announcer.run(ctx)
}

func (cl *Client) trackerUrlKeys(_url string) iter.Seq[clientTrackerKey] {
	return func(yield func(clientTrackerKey) bool) {
		for network := range cl.trackerUrlNetworks(_url) {
			yield(clientTrackerKey{
				Network: network,
				Url:     _url,
			})
		}
	}
}

// This allows us to not split networks if configured so.
func (cl *Client) trackerUrlNetworks(_url string) iter.Seq[string] {
	return func(yield func(string) bool) {
		if len(_url) > 0 && _url[0] == '*' {
			_url = _url[1:]
		}
		u, err := url.Parse(_url)
		// Filter this out somewhere else.
		panicif.Err(err)
		switch u.Scheme {
		case "udp":
			yield("udp4")
			yield("udp6")
		default:
			yield(u.Scheme)
		}
	}
}
