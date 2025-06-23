Rough notes on how requests are determined.

piece ordering cache:

- pieces are grouped by shared storage capacity and ordered by priority, availability, index and then infohash.
- if a torrent does not have a storage cap, pieces are also filtered by whether requests should currently be made for them. this is because for torrents without a storage cap, there's no need to consider pieces that won't be requested.

building a list of candidate requests for a peer:

- pieces are scanned in order of the pre-sorted order for the storage group.
- scanning stops when the cumulative piece length so far exceeds the storage capacity.
- pieces are filtered by whether requests should currently be made for them (hashing, marking, already complete, etc.)
- if requests were added to the consideration list, or the piece was in a partial state, the piece length is added to a cumulative total of unverified bytes.
- if the cumulative total of unverified bytes reaches the configured limit (default 64MiB), piece scanning is halted.

applying request state:

- send the appropriate interest message if our interest doesn't match what the peer is seeing
- sort all candidate requests by:
  - allowed fast if we're being choked,
  - piece priority,
  - whether the request is already outstanding to the peer,
  - whether the request is not pending from any peer
  - if the request is outstanding from a peer:
    - how many outstanding requests the existing peer has
    - most recently requested
  - least available piece
