package dht

// A hook function for handling incoming KRPC messages.
// The arguments are the incoming DHTAddr and Msg.
// The outputs are:
//  - Optional replacement Msg for ensuing use in default handlers
//  - Bool indicating whether to skip default handlers
//  - error, which propagates outwards from Server.handleQuery
type KRPCHook func(DHTAddr, Msg) (*Msg, bool, error)
