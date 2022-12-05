package httpTracker

import (
	"fmt"

	"github.com/anacrolix/dht/v2/krpc"

	"github.com/anacrolix/torrent/bencode"
)

type HttpResponse struct {
	FailureReason string `bencode:"failure reason"`
	Interval      int32  `bencode:"interval"`
	TrackerId     string `bencode:"tracker id"`
	Complete      int32  `bencode:"complete"`
	Incomplete    int32  `bencode:"incomplete"`
	Peers         Peers  `bencode:"peers"`
	// BEP 7
	Peers6 krpc.CompactIPv6NodeAddrs `bencode:"peers6"`
}

type Peers struct {
	List    []Peer
	Compact bool
}

func (me Peers) MarshalBencode() ([]byte, error) {
	if me.Compact {
		cnas := make([]krpc.NodeAddr, 0, len(me.List))
		for _, peer := range me.List {
			cnas = append(cnas, krpc.NodeAddr{
				IP:   peer.IP,
				Port: peer.Port,
			})
		}
		return krpc.CompactIPv4NodeAddrs(cnas).MarshalBencode()
	} else {
		return bencode.Marshal(me.List)
	}
}

var (
	_ bencode.Unmarshaler = (*Peers)(nil)
	_ bencode.Marshaler   = Peers{}
)

func (me *Peers) UnmarshalBencode(b []byte) (err error) {
	var _v interface{}
	err = bencode.Unmarshal(b, &_v)
	if err != nil {
		return
	}
	switch v := _v.(type) {
	case string:
		vars.Add("http responses with string peers", 1)
		var cnas krpc.CompactIPv4NodeAddrs
		err = cnas.UnmarshalBinary([]byte(v))
		if err != nil {
			return
		}
		me.Compact = true
		for _, cp := range cnas {
			me.List = append(me.List, Peer{
				IP:   cp.IP[:],
				Port: cp.Port,
			})
		}
		return
	case []interface{}:
		vars.Add("http responses with list peers", 1)
		me.Compact = false
		for _, i := range v {
			var p Peer
			p.FromDictInterface(i.(map[string]interface{}))
			me.List = append(me.List, p)
		}
		return
	default:
		vars.Add("http responses with unhandled peers type", 1)
		err = fmt.Errorf("unsupported type: %T", _v)
		return
	}
}
