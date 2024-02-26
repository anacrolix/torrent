### Literature

* [arvid on writing a fast piece picker](https://blog.libtorrent.org/2011/11/writing-a-fast-piece-picker/)

    Uses C++ for examples.

* [On Piece Selection for Streaming BitTorrent](https://www.diva-portal.org/smash/get/diva2:835742/FULLTEXT01.pdf)
 
  Some simulations by some Swedes on piece selection.

* [A South American paper on peer-selection strategies for uploading](https://arxiv.org/pdf/1402.2187.pdf)

  Has some useful overviews of piece-selection.

### Hole-punching

Holepunching is tracked in Torrent, rather than in Client because if we send a rendezvous message, and subsequently receive a connect message, we do not know if a peer sent a rendezvous message to our relay and we're receiving the connect message for their rendezvous or ours. Relays are not required to respond to rendezvous, so we can't enforce a timeout. If we don't know if who sent the rendezvous that triggered a connect, then we don't know what infohash to use in the handshake. Once we send a rendezvous, and never receive a reply, we would have to always perform handshakes with our original infohash, or always copy the infohash the remote sends. Handling connects by always being the passive side in the handshake won't work since the other side might use the same behaviour and neither will initiate.

If we only perform rendezvous through relays for the same torrent as the relay, then all the handshake can be done actively for all connect messages. All connect messages received from a peer can only be for the same torrent for which we are connected to the peer.

In 2006, approximately 70% of clients were behind NAT (https://web.archive.org/web/20100724011252/http://illuminati.coralcdn.org/stats/). According to https://fosdem.org/2023/schedule/event/network_hole_punching_in_the_wild/,  hole punching (in libp2p) 70% of NAT can be defeated by relay mechanisms.

If either or both peers in a potential peer do not have NAT, or are full cone NAT, then NAT doesn't matter at least for BitTorrent, as both parties are trying to connect to each other and connections will always work in one direction.

The chance that 2 peers can connect to each other would be 1-(badnat)^2, and 1-unrelayable*(badnat)^2 where unrelayable is the chance they can't work even with a relay, and badnat is the chance a peer has a bad NAT (not full cone). For example if unrelayable is 0.3 per the libp2p study, and badnat is 0.5 (i made this up), 92.5% of peers can connect with each other if they use "relay mechanisms", and 75% if they don't. as long as any peers in the swarm are not badnat, they can relay those that are, and and act as super nodes for peers that can't or don't implement hole punching.

The DHT is a bit different: you can't be an active node if you are a badnat, but you can still query the network to get what you need, you just don't contribute to it. It also doesn't matter what the swarm looks like for a given torrent on the DHT, because you don't have to be in the swarm to host its data. all that matters is that there are some peers that aren't badnat that are in the DHT, of which there are millions (for BitTorrent).

- https://blog.ipfs.tech/2022-01-20-libp2p-hole-punching/
- https://www.bittorrent.org/beps/bep_0055.html
- https://github.com/anacrolix/torrent/issues/685
- https://stackoverflow.com/questions/38786438/libutp-%C2%B5tp-and-nat-traversal-udp-hole-punching

### BitTorrent v2

- https://www.bittorrent.org/beps/bep_0052.html

The canonical infohash to use for a torrent will be the v1 infohash, or the short form of the v2 infohash if v1 is not supported. This will apply everywhere that both infohashes are present. If only one 20 byte hash is present, it is always the v1 hash (except in code that interfaces with things that only work with 20 byte hashes, like the DHT).