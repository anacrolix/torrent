package torrent

import (
	"fmt"
	"net/url"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/tracker"
	trHttp "github.com/anacrolix/torrent/tracker/http"
)

type trackerAnnouncer struct {
	trackerClient tracker.Client
	torrentClient *Client
}

func (me *trackerAnnouncer) Run() {
	select {
	case <-me.torrentClient.closed.Done():
	}
}

func (cl *Client) startTrackerAnnouncer(u *url.URL, urlStr string) {
	panicif.NotEq(u.String(), urlStr)
	if g.MapContains(cl.regularTrackerAnnouncers, urlStr) {
		return
	}
	/*
		res, err := tracker.Announce{
			Context:             ctx,
			HttpProxy:           me.t.cl.config.HTTPProxy,
			HttpRequestDirector: me.t.cl.config.HttpRequestDirector,
			DialContext:         me.t.cl.config.TrackerDialContext,
			ListenPacket:        me.t.cl.config.TrackerListenPacket,
			UserAgent:           me.t.cl.config.HTTPUserAgent,
			TrackerUrl:          me.trackerUrl(ip),
			Request:             req,
			HostHeader:          me.u.Host,
			ServerName:          me.u.Hostname(),
			UdpNetwork:          me.u.Scheme,
			ClientIp4:           krpc.NodeAddr{IP: me.t.cl.config.PublicIp4},
			ClientIp6:           krpc.NodeAddr{IP: me.t.cl.config.PublicIp6},
			Logger:              me.t.logger,
		}.Do()

		cl, err := NewClient(me.TrackerUrl, NewClientOpts{
			Http: trHttp.NewClientOpts{
				Proxy:       me.HttpProxy,
				DialContext: me.DialContext,
				ServerName:  me.ServerName,
			},
			UdpNetwork:   me.UdpNetwork,
			Logger:       me.Logger.WithContextValue(fmt.Sprintf("tracker client for %q", me.TrackerUrl)),
			ListenPacket: me.ListenPacket,
		})
		if err != nil {
			return
		}
		defer cl.Close()
		if me.Context == nil {
			// This is just to maintain the old behaviour that should be a timeout of 15s. Users can
			// override it by providing their own Context. See comments elsewhere about longer timeouts
			// acting as rate limiting overloaded trackers.
			ctx, cancel := context.WithTimeout(context.Background(), DefaultTrackerAnnounceTimeout)
			defer cancel()
			me.Context = ctx
		}
		return cl.Announce(me.Context, me.Request, trHttp.AnnounceOpt{
			UserAgent:           me.UserAgent,
			HostHeader:          me.HostHeader,
			ClientIp4:           me.ClientIp4.IP,
			ClientIp6:           me.ClientIp6.IP,
			HttpRequestDirector: me.HttpRequestDirector,
		})
	*/
	tc, err := tracker.NewClient(urlStr, tracker.NewClientOpts{
		Http: trHttp.NewClientOpts{
			Proxy:       cl.config.HTTPProxy,
			DialContext: cl.config.TrackerDialContext,
			ServerName:  u.Hostname(),
		},
		UdpNetwork:   u.Scheme,
		Logger:       cl.logger.WithContextValue(fmt.Sprintf("tracker client for %q", urlStr)),
		ListenPacket: cl.config.TrackerListenPacket,
	})
	panicif.Err(err)
	ta := &trackerAnnouncer{
		trackerClient: tc,
		torrentClient: cl,
	}
	g.MapMustAssignNew(cl.regularTrackerAnnouncers, urlStr, ta)
	ta.Run()
}

type regularTrackerAnnouncer struct {
	u                     *url.URL
	getLastAnnounceResult func() trackerAnnounceResult
}

func (r regularTrackerAnnouncer) statusLine() string {
	return regularTrackerScraperStatusLine(r.getLastAnnounceResult())
}

func (r regularTrackerAnnouncer) URL() *url.URL {
	return r.u
}

func (r regularTrackerAnnouncer) Stop() {
	// Currently the client-level announcer will just see it was dropped when looking for work.
}

var _ torrentTrackerAnnouncer = regularTrackerAnnouncer{}
