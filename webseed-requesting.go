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
	"slices"
	"strings"
	"sync"
	"time"
	"unique"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/heap"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/internal/extracmp"
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

type webseedRequestHeapElem struct {
	webseedUniqueRequestKey
	webseedRequestOrderValue
	// Not sure this is even worth it now.
	mightHavePartialFiles bool
}

/*
- Go through all the requestable pieces in order of priority, availability, whether there are peer requests, partial, infohash.
- For each piece calculate files involved. Record each file not seen before and the piece index.
- Cancel any outstanding requests that don't match a final file/piece-index pair.
- Initiate missing requests that fit into the available limits.
*/
func (cl *Client) updateWebseedRequests() {
	existingRequests := maps.Collect(cl.iterCurrentWebseedRequestsFromClient())
	panicif.False(maps.Equal(existingRequests, maps.Collect(cl.iterCurrentWebseedRequests())))

	g.MakeMapIfNil(&cl.aprioriMap)
	aprioriMap := cl.aprioriMap
	clear(aprioriMap)
	for uniqueKey, value := range cl.iterPossibleWebseedRequests() {
		//if len(aprioriMap) >= webseedHostRequestConcurrency {
		//	break
		//}
		if g.MapContains(existingRequests, uniqueKey) {
			continue
		}
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
				extracmp.CompareBool(hasPeerConnRequest(cur.startRequest), hasPeerConnRequest(value.startRequest)),
				cmp.Compare(cur.startRequest, value.startRequest),
			) <= 0 {
				continue
			}
		}
		aprioriMap[uniqueKey] = value
	}

	heapSlice := cl.heapSlice[:0]
	requiredCap := len(aprioriMap) + len(existingRequests)
	if cap(heapSlice) < requiredCap {
		heapSlice = slices.Grow(heapSlice, requiredCap-cap(heapSlice))
	}
	defer func() {
		// Will this let GC collect values? If not do we need to clear? :(
		cl.heapSlice = heapSlice[:0]
	}()

	for key, value := range aprioriMap {
		// Should be filtered earlier.
		panicif.True(g.MapContains(existingRequests, key))
		heapSlice = append(heapSlice, webseedRequestHeapElem{
			key,
			webseedRequestOrderValue{
				aprioriMapValue: value,
			},
			key.t.filesInRequestRangeMightBePartial(
				value.startRequest,
				key.t.endRequestForAlignedWebseedResponse(key.sliceIndex),
			),
		})
	}

	// Add remaining existing requests.
	for key, value := range existingRequests {
		// Don't reconsider existing requests that aren't wanted anymore.
		if key.t.dataDownloadDisallowed.IsSet() {
			continue
		}
		wr := value.existingWebseedRequest
		heapSlice = append(heapSlice, webseedRequestHeapElem{
			key,
			value,
			key.t.filesInRequestRangeMightBePartial(wr.next, wr.end),
		})
	}

	aprioriHeap := heap.InterfaceForSlice(
		&heapSlice,
		func(l webseedRequestHeapElem, r webseedRequestHeapElem) bool {
			// Not stable ordering but being sticky to existing webseeds should be enough.
			ret := cmp.Or(
				// Prefer highest priority
				-cmp.Compare(l.priority, r.priority),
				// Then existing requests
				extracmp.CompareBool(l.existingWebseedRequest == nil, r.existingWebseedRequest == nil),
				// Prefer not competing with active peer connections.
				cmp.Compare(len(l.t.conns), len(r.t.conns)),
				// Try to complete partial slices first.
				-extracmp.CompareBool(l.mightHavePartialFiles, r.mightHavePartialFiles),
				// No need to prefer longer files anymore now that we're using slices?
				//// Longer files first.
				//-cmp.Compare(l.longestFile().Unwrap(), r.longestFile().Unwrap()),
				// Easier to debug than infohashes...
				cmp.Compare(l.t.info.Name, r.t.info.Name),
				bytes.Compare(l.t.canonicalShortInfohash()[:], r.t.canonicalShortInfohash()[:]),
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
			sliceIndex: elem.sliceIndex,
		})
		delete(unwantedExistingRequests, requestKey)
	}

	// Cancel any existing requests that are no longer wanted.
	for key, value := range unwantedExistingRequests {
		// Should we skip cancelling requests that are ended and just haven't cleaned up yet?
		value.existingWebseedRequest.Cancel("deprioritized", key.t)
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
			// TODO: Requests aren't limited by the pieces a peer has.
			end := t.getWebseedRequestEnd(begin, request.sliceIndex, debugLogger)
			panicif.LessThanOrEqual(end, begin)

			peer.spawnRequest(begin, end, debugLogger)
		}
	}
}

var shortenWebseedRequests = true

func init() {
	s, ok := os.LookupEnv("TORRENT_SHORTEN_WEBSEED_REQUESTS")
	if !ok {
		return
	}
	shortenWebseedRequests = s != ""
}

func (t *Torrent) getWebseedRequestEnd(begin RequestIndex, slice webseedSliceIndex, debugLogger *slog.Logger) RequestIndex {
	chunkEnd := t.endRequestForAlignedWebseedResponse(slice)
	if !shortenWebseedRequests {
		// Pending fix to pendingPieces matching piece request order due to missing initial pieces
		// checks?
		return chunkEnd
	}
	// Shorten webseed requests to avoid being penalized by webseeds for cancelling requests.
	panicif.False(t.wantReceiveChunk(begin))
	var end = begin + 1
	for ; end < chunkEnd && t.wantReceiveChunk(end); end++ {
	}
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
func (t *Torrent) endRequestForAlignedWebseedResponse(slice webseedSliceIndex) RequestIndex {
	end := min(
		t.maxEndRequest(),
		RequestIndex(slice+1)*t.chunksPerAlignedWebseedResponse())
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
	sliceIndex webseedSliceIndex
	startIndex RequestIndex
}

func (me *plannedWebseedRequest) toChunkedWebseedRequestKey() webseedUniqueRequestKey {
	return webseedUniqueRequestKey{
		url:        me.url,
		t:          me.t,
		sliceIndex: me.sliceIndex,
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

func (me webseedUniqueRequestKey) String() string {
	return fmt.Sprintf("torrent %v: webseed %v: slice %v", me.t, me.url, me.sliceIndex)
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
			if !requestStrategy.GetRequestablePieces(
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
						if !ws.peer.peerHasPiece(pieceIndex) {
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
			) {
				break
			}
		}
	}
}

func (cl *Client) updateWebseedRequestsWithReason(reason updateRequestReason) {
	// Should we wrap this with pprof labels?
	cl.scheduleImmediateWebseedRequestUpdate(reason)
}

// This has awful naming, I'm not quite sure what to call this.
func (cl *Client) yieldKeyAndValue(
	yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool,
	key webseedUniqueRequestKey,
	ar *webseedRequest,
) bool {
	t := key.t
	url := key.url
	hostKey := t.webSeeds[url].hostKey
	// Don't spawn requests before old requests are cancelled.
	if false {
		if ar.cancelled.Load() {
			cl.slogger.Debug("iter current webseed requests: skipped cancelled webseed request")
			// This should prevent overlapping webseed requests that are just filling
			// slots waiting to cancel from conflicting.
			return true
		}
	}
	priority := PiecePriorityNone
	if ar.next < ar.end {
		p := t.piece(t.pieceIndexOfRequestIndex(ar.next))
		priority = p.effectivePriority()
	}
	return yield(
		webseedUniqueRequestKey{
			t:          t,
			sliceIndex: t.requestIndexToWebseedSliceIndex(ar.begin),
			url:        url,
		},
		webseedRequestOrderValue{
			aprioriMapValue{
				priority:     priority,
				costKey:      hostKey,
				startRequest: ar.next,
			},
			ar,
		},
	)
}

func (cl *Client) iterCurrentWebseedRequestsFromClient() iter.Seq2[webseedUniqueRequestKey, webseedRequestOrderValue] {
	return func(yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool) {
		for key, ar := range cl.activeWebseedRequests {
			if !cl.yieldKeyAndValue(yield, key, ar) {
				return
			}
		}
	}
}

// This exists to compare old behaviour with Client active requests.
func (cl *Client) iterCurrentWebseedRequests() iter.Seq2[webseedUniqueRequestKey, webseedRequestOrderValue] {
	return func(yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool) {
		for t := range cl.torrents {
			for url, ws := range t.webSeeds {
				for ar := range ws.activeRequests {
					key := webseedUniqueRequestKey{
						t:          t,
						sliceIndex: t.requestIndexToWebseedSliceIndex(ar.begin),
						url:        url,
					}
					if !cl.yieldKeyAndValue(yield, key, ar) {
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
