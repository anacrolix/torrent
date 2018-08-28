package event

import "github.com/anacrolix/torrent/log"

type logBuilder func(log.Event, string) string

func buildMsg(e log.Event, lbs ...logBuilder) string {
	msg := eventMsg(e)
	for _, lb := range lbs {
		msg = lb(e, msg)
	}

	return msg
}
