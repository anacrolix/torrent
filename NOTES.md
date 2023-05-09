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