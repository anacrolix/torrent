package udp

type ScrapeRequest []InfoHash

type ScrapeResponse []ScrapeInfohashResult

type ScrapeInfohashResult struct {
	// I'm not sure why the fields are named differently for HTTP scrapes.
	// https://www.bittorrent.org/beps/bep_0048.html
	Seeders   int32 `bencode:"complete"`
	Completed int32 `bencode:"downloaded"`
	Leechers  int32 `bencode:"incomplete"`
}
