/*
Package torrent implements a torrent client. Goals include:
 * Configurable data storage, such as file, mmap, and piece-based.
 * Downloading on demand: torrent.Reader will request only the data required to
   satisfy Reads, which is ideal for streaming and torrentfs.

BitTorrent features implemented include:
 * Protocol obfuscation
 * DHT
 * uTP
 * PEX
 * Magnet links
 * IP Blocklists
 * Some IPv6
 * HTTP and UDP tracker clients
*/
package torrent
