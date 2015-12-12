package dht

import "net"

// A hook function for handling incoming KRPC messages.
// Hooks can't pass errors back, so if error logging or
// management within hooks is desired, consider passing
// methods of a custom type as KRPCHooks and use the type
// to handle errors.
// The arguments are the incoming DHTAddr and Msg.
// The outputs are:
//  - Optional replacement Msg for ensuing use in default handlers
//  - Bool indicating whether to propagate msg to default handlers.
type KRPCHook func(*net.Addr, *Node, *Msg) (*Msg, bool)
