package testutil

import (
	"github.com/anacrolix/generics"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/dht/krpc"
	"github.com/james-lawrence/torrent/dht/types"
)

func Int160WithBitSet(bit int) *int160.T {
	var i int160.T
	i.SetBit(7+bit*8, true)
	return &i
}

type addrMaybeId = types.AddrMaybeId

var SampleAddrMaybeIds = []addrMaybeId{
	{},
	{Id: generics.Some(int160.T{})},
	{Id: generics.Some(*Int160WithBitSet(13))},
	{Id: generics.Some(*Int160WithBitSet(12))},
	{Addr: krpc.NewNodeAddrFromIPPort(nil, 1)},
	{
		Id:   generics.Some(*Int160WithBitSet(14)),
		Addr: krpc.NewNodeAddrFromIPPort(nil, 1),
	},
}
