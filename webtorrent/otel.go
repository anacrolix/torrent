package webtorrent

import (
	"context"
	"github.com/pion/webrtc/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName        = "anacrolix.torrent.webtorrent"
	webrtcConnTypeKey = "webtorrent.webrtc.conn.type"
)

func dataChannelStarted(peerConnectionCtx context.Context, dc *webrtc.DataChannel) (dataChannelCtx context.Context, span trace.Span) {
	trace.SpanFromContext(peerConnectionCtx).AddEvent("starting data channel")
	dataChannelCtx, span = otel.Tracer(tracerName).Start(peerConnectionCtx, "DataChannel")
	dc.OnClose(func() {
		span.End()
	})
	return
}
