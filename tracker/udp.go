package tracker

type UDPConnectionRequest struct {
	ConnectionId int64
	Action       int32
	TransctionId int32
}

type UDPAnnounceResponseHeader struct {
	Action        int32
	TransactionId int32
	Interval      int32
	Leechers      int32
	Seeders       int32
}

type UDPAnnounceResponse struct {
	UDPAnnounceResponseHeader
	PeerAddrSlice
}

type PeerAddr struct {
	IP   int32
	Port int16
}

type PeerAddrSlice []PeerAddr
