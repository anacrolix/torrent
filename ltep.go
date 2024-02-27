package torrent

import (
	"fmt"
	"slices"

	g "github.com/anacrolix/generics"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

type LocalLtepProtocolMap struct {
	// 1-based mapping from extension number to extension name (subtract one from the extension ID
	// to find the corresponding protocol name). The first LocalLtepProtocolBuiltinCount of these
	// are use builtin handlers. If you want to handle builtin protocols yourself, you would move
	// them above the threshold. You can disable them by removing them entirely, and add your own.
	// These changes should be done in the PeerConnAdded callback.
	Index []pp.ExtensionName
	// How many of the protocols are using the builtin handlers.
	NumBuiltin int
}

func (me *LocalLtepProtocolMap) toSupportedExtensionDict() (m map[pp.ExtensionName]pp.ExtensionNumber) {
	g.MakeMapWithCap(&m, len(me.Index))
	for i, name := range me.Index {
		old := g.MapInsert(m, name, pp.ExtensionNumber(i+1))
		if old.Ok {
			panic(fmt.Sprintf("extension %q already defined with id %v", name, old.Value))
		}
	}
	return
}

// Returns the local extension name for the given ID. If builtin is true, the implementation intends
// to handle it itself. For incoming messages with extension ID 0, the message is a handshake, and
// should be treated specially.
func (me *LocalLtepProtocolMap) LookupId(id pp.ExtensionNumber) (name pp.ExtensionName, builtin bool, err error) {
	if id == 0 {
		err = fmt.Errorf("extension ID 0 is handshake")
		builtin = true
		return
	}
	protocolIndex := int(id - 1)
	if protocolIndex >= len(me.Index) {
		err = fmt.Errorf("unexpected extended message ID: %v", id)
		return
	}
	builtin = protocolIndex < me.NumBuiltin
	name = me.Index[protocolIndex]
	return
}

func (me *LocalLtepProtocolMap) builtin() []pp.ExtensionName {
	return me.Index[:me.NumBuiltin]
}

func (me *LocalLtepProtocolMap) user() []pp.ExtensionName {
	return me.Index[me.NumBuiltin:]
}

func (me *LocalLtepProtocolMap) AddUserProtocol(name pp.ExtensionName) {
	builtin := slices.DeleteFunc(me.builtin(), func(delName pp.ExtensionName) bool {
		return delName == name
	})
	user := slices.DeleteFunc(me.user(), func(delName pp.ExtensionName) bool {
		return delName == name
	})
	me.Index = append(append(builtin, user...), name)
	me.NumBuiltin = len(builtin)
}
