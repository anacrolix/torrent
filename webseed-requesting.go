package torrent

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"time"
	"unique"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/heap"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/webseed"
)

// Default is based on experience with CloudFlare.
var webseedHostRequestConcurrency = initIntFromEnv("TORRENT_WEBSEED_HOST_REQUEST_CONCURRENCY", 25, 0)

type (
	webseedHostKey       string
	webseedHostKeyHandle = unique.Handle[webseedHostKey]
	webseedUrlKey        unique.Handle[string]
)

func (me webseedUrlKey) Value() string {
	return unique.Handle[string](me).Value()
}

func (me webseedUrlKey) String() string {
	return me.Value()
}

/*
- Go through all the requestable pieces in order of priority, availability, whether there are peer requests, partial, infohash.
- For each piece calculate files involved. Record each file not seen before and the piece index.
- Cancel any outstanding requests that don't match a final file/piece-index pair.
- Initiate missing requests that fit into the available limits.
*/
func (cl *Client) updateWebseedRequests() {
	aprioriMap := make(map[webseedUniqueRequestKey]aprioriMapValue)
	for uniqueKey, value := range cl.iterPossibleWebseedRequests() {
		cur, ok := aprioriMap[uniqueKey]
		if ok {
			// Shared in the lookup above.
			t := uniqueKey.t
			// TODO: Change to "slice has requests"
			hasPeerConnRequest := func(reqIndex RequestIndex) bool {
				return t.requestingPeer(reqIndex) != nil
			}
			// Skip the webseed request unless it has a higher priority, is less requested by peer
			// conns, or has a lower start offset. Including peer conn requests here will bump
			// webseed requests in favour of peer conns unless there's nothing else to do.
			if cmp.Or(
				cmp.Compare(value.priority, cur.priority),
				compareBool(hasPeerConnRequest(cur.startRequest), hasPeerConnRequest(value.startRequest)),
				cmp.Compare(cur.startRequest, value.startRequest),
			) <= 0 {
				continue
			}
		}
		aprioriMap[uniqueKey] = value
	}
	// This includes startRequest in the key. This means multiple webseed requests can exist in the
	// same webseed request slice.
	existingRequests := make(map[webseedUniqueRequestKey]webseedRequestOrderValue)
	existingRequestCount := 0
	// TODO: Maintain a cache of active requests in the Client, and use them to filter proposed
	// requests (same result but less allocations).
	for key, value := range cl.iterCurrentWebseedRequests() {
		existingRequests[key] = value
		existingRequestCount++
	}
	// Check "active" current webseed request cardinality matches expectation.
	panicif.NotEq(len(existingRequests), existingRequestCount)
	// We don't need the value but maybe cloning is just faster anyway?
	unusedExistingRequests := maps.Clone(existingRequests)
	type heapElem struct {
		webseedUniqueRequestKey
		webseedRequestOrderValue
		// Not sure this is even worth it now.
		mightHavePartialFiles bool
	}
	// Build the request heap, merging existing requests if they match.
	heapSlice := make([]heapElem, 0, len(aprioriMap)+len(existingRequests))
	for key, value := range aprioriMap {
		if g.MapContains(existingRequests, key) {
			// Prefer the existing request always
			continue
		}
		heapSlice = append(heapSlice, heapElem{
			key,
			webseedRequestOrderValue{
				aprioriMapValue: value,
			},
			key.t.filesInRequestRangeMightBePartial(
				value.startRequest,
				key.t.endRequestForAlignedWebseedResponse(value.startRequest),
			),
		})
	}
	// Add remaining existing requests.
	for key, value := range unusedExistingRequests {
		// Don't reconsider existing requests that aren't wanted anymore.
		if key.t.dataDownloadDisallowed.IsSet() {
			continue
		}
		wr := value.existingWebseedRequest
		heapSlice = append(heapSlice, heapElem{
			key,
			existingRequests[key],
			key.t.filesInRequestRangeMightBePartial(wr.next, wr.end),
		})
	}
	aprioriHeap := heap.InterfaceForSlice(
		&heapSlice,
		func(l heapElem, r heapElem) bool {
			// Not stable ordering but being sticky to existing webseeds should be enough.
			ret := cmp.Or(
				// Prefer highest priority
				-cmp.Compare(l.priority, r.priority),
				// Then existing requests
				compareBool(l.existingWebseedRequest == nil, r.existingWebseedRequest == nil),
				// Prefer not competing with active peer connections.
				compareBool(len(l.t.conns) > 0, len(r.t.conns) > 0),
				// Try to complete partial slices first.
				-compareBool(l.mightHavePartialFiles, r.mightHavePartialFiles),
				// No need to prefer longer files anymore now that we're using slices?
				//// Longer files first.
				//-cmp.Compare(l.longestFile().Unwrap(), r.longestFile().Unwrap()),
				// Easier to debug than infohashes...
				cmp.Compare(l.t.info.Name, r.t.info.Name),
				bytes.Compare(l.t.canonicalShortInfohash()[:], r.t.canonicalShortInfohash()[:]),
				// It's possible for 2 heap elements to have the same slice index from the same
				// torrent, but they'll differ in existingWebseedRequest and be sorted before this.
				// Doing earlier chunks first means more compact files for partial file hashing.
				cmp.Compare(l.sliceIndex, r.sliceIndex),
			)
			// Requests should be unique unless they're for different URLs.
			if ret == 0 && l.url == r.url {
				cfg := spew.NewDefaultConfig()
				cfg.Dump(l)
				cfg.Dump(r)
				panic("webseed request heap ordering is not stable")
			}
			return ret < 0
		},
	)

	unwantedExistingRequests := maps.Clone(existingRequests)

	heap.Init(aprioriHeap)
	var plan webseedRequestPlan
	// Could also return early here if all known costKeys are fully assigned.
	for aprioriHeap.Len() > 0 {
		elem := heap.Pop(aprioriHeap)
		// Pulling the pregenerated form avoids unique.Handle, and possible URL parsing and error
		// handling overhead. Need the value to avoid looking this up again.
		costKey := elem.costKey
		panicif.Zero(costKey)
		if elem.existingWebseedRequest == nil {
			// Existing requests might be within the allowed discard range.
			panicif.Eq(elem.priority, PiecePriorityNone)
		}
		panicif.True(elem.t.dataDownloadDisallowed.IsSet())
		panicif.True(elem.t.closed.IsSet())
		if len(plan.byCost[costKey]) >= webseedHostRequestConcurrency {
			continue
		}
		g.MakeMapIfNil(&plan.byCost)
		requestKey := elem.webseedUniqueRequestKey
		plan.byCost[costKey] = append(plan.byCost[costKey], plannedWebseedRequest{
			url:        elem.url,
			t:          elem.t,
			startIndex: elem.startRequest,
		})
		delete(unwantedExistingRequests, requestKey)
	}

	// Cancel any existing requests that are no longer wanted.
	for _, value := range unwantedExistingRequests {
		value.existingWebseedRequest.Cancel("deprioritized")
	}

	printPlan := sync.OnceFunc(func() {
		if webseed.PrintDebug {
			//fmt.Println(plan)
			//fmt.Println(formatMap(existingRequests))
		}
	})

	// TODO: Do we deduplicate requests across different webseeds?

	for costKey, plannedRequests := range plan.byCost {
		for _, request := range plannedRequests {
			// This could happen if a request is cancelled but hasn't removed itself from the active
			// list yet. This helps with backpressure as the requests can sleep to rate limit.
			if !cl.underWebSeedHttpRequestLimit(costKey) {
				break
			}
			existingRequestKey := request.toChunkedWebseedRequestKey()
			if g.MapContains(existingRequests, existingRequestKey) {
				// A request exists to the webseed slice already. This doesn't check the request
				// indexes match.

				// Check we didn't just cancel the same request.
				panicif.True(g.MapContains(unwantedExistingRequests, existingRequestKey))
				continue
			}
			t := request.t
			peer := t.webSeeds[request.url]
			panicif.NotEq(peer.hostKey, costKey)
			printPlan()

			debugLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
				Level:     slog.LevelDebug,
				AddSource: true,
			})).With(
				"webseedUrl", request.url,
				"webseedChunkIndex", request.sliceIndex)

			begin := request.startIndex
			end := t.getWebseedRequestEnd(begin, debugLogger)
			panicif.LessThanOrEqual(end, begin)

			peer.spawnRequest(begin, end, debugLogger)
		}
	}
}

func (t *Torrent) getWebseedRequestEnd(begin RequestIndex, debugLogger *slog.Logger) RequestIndex {
	chunkEnd := t.endRequestForAlignedWebseedResponse(begin)
	if true {
		// Pending fix to pendingPieces matching piece request order due to missing initial pieces
		// checks?
		return chunkEnd
	}
	panicif.False(t.wantReceiveChunk(begin))
	last := begin
	for {
		if !t.wantReceiveChunk(last) {
			break
		}
		if last >= chunkEnd-1 {
			break
		}
		last++
	}
	end := last + 1
	panicif.GreaterThan(end, chunkEnd)
	if webseed.PrintDebug && end != chunkEnd {
		debugLogger.Debug(
			"shortened webseed request",
			"from", endExclusiveString(begin, chunkEnd),
			"to", endExclusiveString(begin, end))
	}
	return end
}

// Cloudflare caches up to 512 MB responses by default. This is also an alignment. Making this
// smaller will allow requests to complete a smaller set of files faster.
var webseedRequestChunkSize = initUIntFromEnv[uint64]("TORRENT_WEBSEED_REQUEST_CHUNK_SIZE", 64<<20, 64)

// Can return the same as start if the request is at the end of the torrent.
func (t *Torrent) endRequestForAlignedWebseedResponse(start RequestIndex) RequestIndex {
	end := min(t.maxEndRequest(), nextMultiple(start, t.chunksPerAlignedWebseedResponse()))
	return end
}

func (t *Torrent) chunksPerAlignedWebseedResponse() RequestIndex {
	// This is the same as webseedRequestChunkSize, but in terms of RequestIndex.
	return RequestIndex(webseedRequestChunkSize / t.chunkSize.Uint64())
}

func (t *Torrent) requestIndexToWebseedSliceIndex(requestIndex RequestIndex) webseedSliceIndex {
	return webseedSliceIndex(requestIndex / t.chunksPerAlignedWebseedResponse())
}

func (cl *Client) dumpCurrentWebseedRequests() {
	if webseed.PrintDebug {
		fmt.Println("current webseed requests:")
		for key, value := range cl.iterCurrentWebseedRequests() {
			fmt.Printf("\t%v: %v, priority %v\n", key, value.existingWebseedRequest, value.priority)
		}
	}
}

type webseedRequestPlan struct {
	byCost map[webseedHostKeyHandle][]plannedWebseedRequest
}

// Needed components to generate a webseed request.
type plannedWebseedRequest struct {
	url        webseedUrlKey
	t          *Torrent
	startIndex RequestIndex
}

func (me *plannedWebseedRequest) sliceIndex() webseedSliceIndex {
	return me.t.requestIndexToWebseedSliceIndex(me.startIndex)
}

func (me *plannedWebseedRequest) toChunkedWebseedRequestKey() webseedUniqueRequestKey {
	return webseedUniqueRequestKey{
		url:        me.url,
		t:          me.t,
		sliceIndex: me.sliceIndex(),
	}
}

func (me webseedRequestPlan) String() string {
	var sb strings.Builder
	for costKey, requestKeys := range me.byCost {
		fmt.Fprintf(&sb, "%v\n", costKey.Value())
		for _, requestKey := range requestKeys {
			fmt.Fprintf(&sb, "\t%v\n", requestKey)
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// Distinct webseed request data when different offsets are not allowed.
type webseedUniqueRequestKey struct {
	url        webseedUrlKey
	t          *Torrent
	sliceIndex webseedSliceIndex
}

type aprioriMapValue struct {
	costKey      webseedHostKeyHandle
	priority     PiecePriority
	startRequest RequestIndex
}

func (me *webseedUniqueRequestKey) String() string {
	return fmt.Sprintf("slice %v from %v", me.sliceIndex, me.url)
}

// Non-distinct proposed webseed request data.
type webseedRequestOrderValue struct {
	aprioriMapValue
	// Used for cancellation if this is deprioritized. Also, a faster way to sort for existing
	// requests.
	existingWebseedRequest *webseedRequest
}

func (me webseedRequestOrderValue) String() string {
	return fmt.Sprintf("%#v", me)
}

// Yields possible webseed requests by piece. Caller should filter and prioritize these.
func (cl *Client) iterPossibleWebseedRequests() iter.Seq2[webseedUniqueRequestKey, aprioriMapValue] {
	return func(yield func(webseedUniqueRequestKey, aprioriMapValue) bool) {
		for key, value := range cl.pieceRequestOrder {
			input := key.getRequestStrategyInput(cl)
			requestStrategy.GetRequestablePieces(
				input,
				value.pieces,
				func(ih metainfo.Hash, pieceIndex int, orderState requestStrategy.PieceRequestOrderState) bool {
					t := cl.torrentsByShortHash[ih]
					if len(t.webSeeds) == 0 {
						return true
					}
					p := t.piece(pieceIndex)
					cleanOpt := p.firstCleanChunk()
					if !cleanOpt.Ok {
						return true
					}
					// Pretty sure we want this and not the order state priority. That one is for
					// client piece request order and ignores other states like hashing, marking
					// etc. Order state priority would be faster otherwise.
					priority := p.effectivePriority()
					firstRequest := p.requestIndexBegin() + cleanOpt.Value
					panicif.GreaterThanOrEqual(firstRequest, t.maxEndRequest())
					webseedSliceIndex := t.requestIndexToWebseedSliceIndex(firstRequest)
					for url, ws := range t.webSeeds {
						if ws.suspended() {
							continue
						}
						// Return value from this function (RequestPieceFunc) doesn't terminate
						// iteration, so propagate that to not handling the yield return value.
						if !yield(
							webseedUniqueRequestKey{
								t:          t,
								sliceIndex: webseedSliceIndex,
								url:        url,
							},
							aprioriMapValue{
								priority:     priority,
								costKey:      ws.hostKey,
								startRequest: firstRequest,
							},
						) {
							return false
						}
					}
					return true
				},
			)
		}
	}

}

func (cl *Client) updateWebseedRequestsWithReason(reason updateRequestReason) {
	// Should we wrap this with pprof labels?
	cl.scheduleImmediateWebseedRequestUpdate(reason)
}

func (cl *Client) iterCurrentWebseedRequests() iter.Seq2[webseedUniqueRequestKey, webseedRequestOrderValue] {
	return func(yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool) {
		// TODO: This entire thing can be a single map on Client ("active webseed requests").
		for t := range cl.torrents {
			for url, ws := range t.webSeeds {
				for ar := range ws.activeRequests {
					if ar.next >= ar.end {
						// This request is done, so don't yield it.
						continue
					}
					// Don't spawn requests before old requests are cancelled.
					if false {
						if ar.cancelled.Load() {
							cl.slogger.Debug("iter current webseed requests: skipped cancelled webseed request")
							// This should prevent overlapping webseed requests that are just filling
							// slots waiting to cancel from conflicting.
							continue
						}
					}
					p := t.piece(t.pieceIndexOfRequestIndex(ar.next))
					if !yield(
						webseedUniqueRequestKey{
							t:          t,
							sliceIndex: t.requestIndexToWebseedSliceIndex(ar.next),
							url:        url,
						},
						webseedRequestOrderValue{
							aprioriMapValue{
								priority:     p.effectivePriority(),
								costKey:      ws.hostKey,
								startRequest: ar.next,
							},
							ar,
						},
					) {
						return
					}
				}
			}
		}
	}
}

func (cl *Client) scheduleImmediateWebseedRequestUpdate(reason updateRequestReason) {
	if !cl.webseedRequestTimer.Stop() {
		// Timer function already running, let it do its thing.
		return
	}
	// Set the timer to fire right away (this will coalesce consecutive updates without forcing an
	// update on every call to this method). Since we're holding the Client lock, and we cancelled
	// the timer, and it wasn't active, nobody else should have reset it before us. Do we need to
	// introduce a "reason" field here, (albeit Client-level?).
	cl.webseedUpdateReason = cmp.Or(cl.webseedUpdateReason, reason)
	panicif.True(cl.webseedRequestTimer.Reset(0))
}

func (cl *Client) updateWebseedRequestsTimerFunc() {
	if cl.closed.IsSet() {
		return
	}
	// This won't get set elsewhere if the timer has fired, which it has for us to be here.
	cl.webseedUpdateReason = cmp.Or(cl.webseedUpdateReason, "timer")
	cl.lock()
	defer cl.unlock()
	cl.updateWebseedRequestsAndResetTimer()
}

func (cl *Client) updateWebseedRequestsAndResetTimer() {
	pprof.Do(context.Background(), pprof.Labels(
		"reason", string(cl.webseedUpdateReason),
	), func(_ context.Context) {
		started := time.Now()
		reason := cl.webseedUpdateReason
		cl.webseedUpdateReason = ""
		cl.updateWebseedRequests()
		panicif.NotZero(cl.webseedUpdateReason)
		if webseed.PrintDebug {
			now := time.Now()
			fmt.Printf("%v: updateWebseedRequests took %v (reason: %v)\n", now, now.Sub(started), reason)
		}
	})
	// Timer should always be stopped before the last call. TODO: Don't reset timer if there's
	// nothing to do (no possible requests in update).
	panicif.True(cl.webseedRequestTimer.Reset(webseedRequestUpdateTimerInterval))

}

type endExclusive[T any] struct {
	start, end T
}

func (me endExclusive[T]) String() string {
	return fmt.Sprintf("[%v-%v)", me.start, me.end)
}

func endExclusiveString[T any](start, end T) string {
	return endExclusive[T]{start, end}.String()
}
