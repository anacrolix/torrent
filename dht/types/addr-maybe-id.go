package types

import (
	"fmt"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/multiless"

	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/krpc"
)

func AddrMaybeIdSliceFromNodeInfoSlice(nis []krpc.NodeInfo) (ret []AddrMaybeId) {
	ret = make([]AddrMaybeId, 0, len(nis))
	for _, ni := range nis {
		id := int160.FromByteArray(ni.ID)
		ret = append(ret, AddrMaybeId{
			Addr: ni.Addr.ToNodeAddrPort(),
			Id:   generics.Some(id),
		})
	}
	return
}

type AddrMaybeId struct {
	Addr krpc.NodeAddrPort
	Id   generics.Option[int160.T]
}

func (me AddrMaybeId) TryIntoNodeInfo() (ret *krpc.NodeInfo) {
	if !me.Id.Ok {
		return nil
	}
	return &krpc.NodeInfo{
		ID:   me.Id.Value.AsByteArray(),
		Addr: me.Addr.ToNodeAddr(),
	}
}

func (me *AddrMaybeId) FromNodeInfo(ni krpc.NodeInfo) {
	id := int160.FromByteArray(ni.ID)
	*me = AddrMaybeId{
		Addr: ni.Addr.ToNodeAddrPort(),
		Id:   generics.Some(id),
	}
}

func (me AddrMaybeId) String() string {
	if !me.Id.Ok {
		return fmt.Sprintf("unknown id at %s", me.Addr)
	} else {
		return fmt.Sprintf("%v at %v", me.Id.Value, me.Addr)
	}
}

func (l AddrMaybeId) CloserThan(r AddrMaybeId, target int160.T) bool {
	ml := multiless.New().Bool(!l.Id.Ok, !r.Id.Ok)
	if l.Id.Ok && r.Id.Ok {
		ml = ml.Cmp(l.Id.Value.Distance(target).Cmp(r.Id.Value.Distance(target)))
	}
	if !ml.Ok() {
		ml = ml.Cmp(l.Addr.Addr().Compare(r.Addr.Addr()))
		ml = multiless.EagerOrdered(ml, l.Addr.Port(), r.Addr.Port())
	}
	return ml.Less()
}
