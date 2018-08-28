package event

import (
	"sync"

	"github.com/anacrolix/torrent/log"
)

// BaseEvent is an internal event type used for log
type BaseEvent struct {
	*sync.Mutex
	data *map[string]interface{}
}

func eventMsg(e log.Event) string {
	d := *e.Data()

	var ifc interface{}
	var msg string
	var ok bool
	if ifc, ok = d["msg"]; ok {
		msg, ok = ifc.(string)
	}
	if !ok {
		msg = ""
	}

	return msg
}

// String Base string method reeturns msg
func (e BaseEvent) String() string {
	return buildMsg(e)
}

// Data returns map of data associated with event
func (e BaseEvent) Data() *map[string]interface{} {
	return e.data
}

// AddValue adds a KV pair of data to the event (prevent adding of msg or level)
func (e BaseEvent) AddValue(k string, v interface{}) {
	if k == "msg" {
		return
	}

	e.Lock()
	defer e.Unlock()

	d := *e.data
	d[k] = v
}
