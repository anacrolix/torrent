package torrent

import (
	"cmp"
	"iter"
	"maps"
	"unique"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/heap"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/metainfo"
)

const defaultRequestsPerWebseedHost = 5

type (
	webseedHostKey       string
	webseedHostKeyHandle = unique.Handle[webseedHostKey]
	webseedUrlKey        string
)

/*
- Go through all the requestable pieces in order of priority, availability, whether there are peer requests, partial, infohash.
- For each piece calculate files involved. Record each file not seen before and the piece index.
- Cancel any outstanding requests that don't match a final file/piece-index pair.
- Initiate missing requests that fit into the available limits.

This was a globally aware webseed requestor algorithm that is probably going to be abandoned.
*/
func (cl *Client) abandonedUpdateWebSeedRequests() {
	type aprioriMapValue struct {
		startOffset int64
		webseedRequestOrderValue
	}
	aprioriMap := make(map[aprioriWebseedRequestKey]aprioriMapValue)
	for uniqueKey, value := range cl.iterWebseed() {
		cur, ok := aprioriMap[uniqueKey.aprioriWebseedRequestKey]
		if !ok || cmp.Or(
			cmp.Compare(value.priority, cur.priority),
			cmp.Compare(cur.startOffset, uniqueKey.startOffset),
		) > 0 {
			aprioriMap[uniqueKey.aprioriWebseedRequestKey] = aprioriMapValue{uniqueKey.startOffset, value}
		}
	}
	existingRequests := maps.Collect(cl.iterCurrentWebseedRequests())
	// TODO: Try maps.Clone here? We don't need the value but maybe cloning is just faster anyway?
	unusedExistingRequests := make(map[webseedUniqueRequestKey]struct{}, len(existingRequests))
	for key := range existingRequests {
		unusedExistingRequests[key] = struct{}{}
	}
	type heapElem struct {
		webseedUniqueRequestKey
		webseedRequestOrderValue
	}
	// Build the request heap, merging existing requests if they match.
	heapSlice := make([]heapElem, 0, len(aprioriMap)+len(existingRequests))
	for key, value := range aprioriMap {
		fullKey := webseedUniqueRequestKey{key, value.startOffset}
		heapValue := value.webseedRequestOrderValue
		// If there's a matching existing request, make sure to include a reference to it in the
		// heap value and deduplicate it.
		existingValue, ok := existingRequests[fullKey]
		if ok {
			// Priorities should have been generated the same.
			panicif.NotEq(value.priority, existingValue.priority)
			// A-priori map should not have existing request associated with it. TODO: a-priori map
			// value shouldn't need some fields.
			panicif.NotZero(value.existingWebseedRequest)
			heapValue.existingWebseedRequest = existingValue.existingWebseedRequest
			// Now the values should match exactly.
			panicif.NotEq(heapValue, existingValue)
			g.MustDelete(unusedExistingRequests, fullKey)
		}
		heapSlice = append(heapSlice, heapElem{
			fullKey,
			heapValue,
		})
	}
	// Add remaining existing requests.
	for key := range unusedExistingRequests {
		heapSlice = append(heapSlice, heapElem{key, existingRequests[key]})
	}
	aprioriHeap := heap.InterfaceForSlice(
		&heapSlice,
		func(l heapElem, r heapElem) bool {
			// Prefer the highest priority, then existing requests, then longest remaining file extent.
			return cmp.Or(
				cmp.Compare(l.priority, r.priority),
				// Existing requests are assigned the priority of the piece they're reading next.
				compareBool(l.existingWebseedRequest == nil, r.existingWebseedRequest == nil),
				// This won't thrash because we already preferred existing requests, so we'll finish out small extents.
				-cmp.Compare(
					l.t.Files()[l.fileIndex].length-l.startOffset,
					r.t.Files()[r.fileIndex].length-r.startOffset),
			) < 0
		},
	)

	unwantedExistingRequests := maps.Clone(existingRequests)

	heap.Init(aprioriHeap)
	var plan webseedRequestPlan
	for aprioriHeap.Len() > 0 {
		elem := heap.Pop(aprioriHeap)
		// Pulling the pregenerated form avoids unique.Handle, and possible URL parsing and error
		// handling overhead. Need the value to avoid looking this up again.
		costKey := elem.costKey
		panicif.Zero(costKey)
		if len(plan.byCost[costKey]) >= defaultRequestsPerWebseedHost {
			continue
		}
		g.MakeMapIfNil(&plan.byCost)
		requestKey := elem.webseedUniqueRequestKey
		plan.byCost[costKey] = append(plan.byCost[costKey], requestKey)
		delete(unwantedExistingRequests, requestKey)
	}

	// Cancel any existing requests that are no longer wanted.
	for key, value := range unwantedExistingRequests {
		key.t.slogger().Debug("cancelling deprioritized existing webseed request", "webseedUrl", key.url, "fileIndex", key.fileIndex)
		value.existingWebseedRequest.Cancel()
	}

	for _, requestKeys := range plan.byCost {
		for _, requestKey := range requestKeys {
			if g.MapContains(existingRequests, requestKey) {
				continue
			}
			t := requestKey.t
			// Run the request to the end of the file for now. TODO: Set a reasonable end so the
			// remote doesn't oversend.
			t.webSeeds[requestKey.url].spawnRequest(
				t.getRequestIndexContainingOffset(requestKey.startOffset),
				t.endRequestIndexForFileIndex(requestKey.fileIndex))
		}
	}
}

type webseedRequestPlan struct {
	byCost map[webseedHostKeyHandle][]webseedUniqueRequestKey
}

type aprioriWebseedRequestKey struct {
	t         *Torrent
	fileIndex int
	url       webseedUrlKey
}

// To allow multiple requests to the object.
type webseedUniqueRequestKey struct {
	aprioriWebseedRequestKey
	startOffset int64
}

type webseedRequestOrderValue struct {
	priority PiecePriority
	// Used for cancellation if this is deprioritized. Also might be a faster way to sort for
	// existing requests.
	existingWebseedRequest *webseedRequest
	costKey                webseedHostKeyHandle
}

// Yields possible webseed requests by piece. Caller should filter and prioritize these. TODO:
// Doesn't handle dirty chunks.
func (cl *Client) iterWebseed() iter.Seq2[webseedUniqueRequestKey, webseedRequestOrderValue] {
	return func(yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool) {
		for key, value := range cl.pieceRequestOrder {
			input := key.getRequestStrategyInput(cl)
			requestStrategy.GetRequestablePieces(
				input,
				value.pieces,
				func(ih metainfo.Hash, pieceIndex int, orderState requestStrategy.PieceRequestOrderState) bool {
					t := cl.torrentsByShortHash[ih]
					for i, e := range cl.torrentsByShortHash[ih].piece(pieceIndex).fileExtents() {
						for url, ws := range t.webSeeds {
							yield(
								webseedUniqueRequestKey{
									aprioriWebseedRequestKey{
										t:         t,
										fileIndex: i,
										url:       url,
									},
									e.Start,
								},
								webseedRequestOrderValue{
									priority: orderState.Priority,
									costKey:  ws.hostKey,
								},
							)
						}
					}
					// Pieces iterated here are only to select webseed requests. There's no guarantee they're chosen.
					return false
				},
			)
		}
	}

}

func (cl *Client) updateWebSeedRequests(reason updateRequestReason) {
	for t := range cl.torrents {
		for _, p := range t.webSeeds {
			p.peer.updateRequestsWithReason(reason)
		}
	}
}

func (cl *Client) iterCurrentWebseedRequests() iter.Seq2[webseedUniqueRequestKey, webseedRequestOrderValue] {
	return func(yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool) {
		for t := range cl.torrents {
			for url, ws := range t.webSeeds {
				for ar := range ws.activeRequests {
					off := t.requestIndexBegin(ar.next)
					opt := t.info.FileSegmentsIndex().LocateOffset(off)
					if !opt.Ok {
						continue
					}
					p := t.pieceForOffset(off)
					if !yield(
						webseedUniqueRequestKey{
							aprioriWebseedRequestKey{
								t:         t,
								fileIndex: opt.Value.Index,
								url:       url,
							},
							opt.Value.Offset,
						},
						webseedRequestOrderValue{
							priority:               p.effectivePriority(),
							existingWebseedRequest: ar,
							costKey:                ws.hostKey,
						},
					) {
						return
					}
				}
			}
		}
	}
}

func (cl *Client) updateWebseedRequests() {
	cl.abandonedUpdateWebSeedRequests()
	// Should have already run to get here.
	cl.webseedRequestTimer.Reset(webseedRequestUpdateTimerInterval)
}

func (cl *Client) updateWebseedRequestsTimerFunc() {
	cl.lock()
	defer cl.unlock()
	cl.updateWebseedRequests()
}
