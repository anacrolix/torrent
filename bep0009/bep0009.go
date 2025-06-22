package bep0009

type MetadataResponse struct {
	Type  int `bencode:"msg_type"`
	Index int `bencode:"piece"`      // index of the metadata piece.
	Total int `bencode:"total_size"` // total byte size of metadata
}

type MetadataRequest struct {
	Type  int `bencode:"msg_type"`
	Index int `bencode:"piece"` // index of the metadata piece.
}
