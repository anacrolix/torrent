package torrent

import (
	"cmp"
	"context"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"
	"unique"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/heap"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/webseed"
)

var webseedHostRequestConcurrency int

func init() {
	i64, err := strconv.ParseInt(cmp.Or(os.Getenv("TORRENT_WEBSEED_HOST_REQUEST_CONCURRENCY"), "10"), 10, 0)
	panicif.Err(err)
	webseedHostRequestConcurrency = int(i64)
}

type (
	webseedHostKey       string
	webseedHostKeyHandle = unique.Handle[webseedHostKey]
	webseedUrlKey        unique.Handle[string]
)

/*
- Go through all the requestable pieces in order of priority, availability, whether there are peer requests, partial, infohash.
- For each piece calculate files involved. Record each file not seen before and the piece index.
- Cancel any outstanding requests that don't match a final file/piece-index pair.
- Initiate missing requests that fit into the available limits.
*/
func (cl *Client) updateWebseedRequests() {
	type aprioriMapValue struct {
		startIndex RequestIndex
		webseedRequestOrderValue
	}
	aprioriMap := make(map[aprioriWebseedRequestKey]aprioriMapValue)
	for uniqueKey, value := range cl.iterPossibleWebseedRequests() {
		cur, ok := aprioriMap[uniqueKey.aprioriWebseedRequestKey]
		if ok {
			// Shared in the lookup above.
			t := uniqueKey.t
			hasPeerConnRequest := func(reqIndex RequestIndex) bool {
				return t.requestingPeer(reqIndex) != nil
			}
			// Skip the webseed request unless it has a higher priority, is less requested by peer
			// conns, or has a lower start offset. Including peer conn requests here will bump
			// webseed requests in favour of peer conns unless there's nothing else to do.
			if cmp.Or(
				cmp.Compare(value.priority, cur.priority),
				compareBool(hasPeerConnRequest(cur.startIndex), hasPeerConnRequest(uniqueKey.startRequest)),
				cmp.Compare(cur.startIndex, uniqueKey.startRequest),
			) <= 0 {
				continue
			}
		}
		aprioriMap[uniqueKey.aprioriWebseedRequestKey] = aprioriMapValue{uniqueKey.startRequest, value}
	}
	existingRequests := maps.Collect(cl.iterCurrentWebseedRequests())
	// We don't need the value but maybe cloning is just faster anyway?
	unusedExistingRequests := maps.Clone(existingRequests)
	type heapElem struct {
		webseedUniqueRequestKey
		webseedRequestOrderValue
		mightHavePartialFiles bool
	}
	// Build the request heap, merging existing requests if they match.
	heapSlice := make([]heapElem, 0, len(aprioriMap)+len(existingRequests))
	for key, value := range aprioriMap {
		fullKey := webseedUniqueRequestKey{key, value.startIndex}
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
			fullKey.mightHavePartialFiles(),
		})
	}
	// Add remaining existing requests.
	for key := range unusedExistingRequests {
		// Don't reconsider existing requests that aren't wanted anymore.
		if key.t.dataDownloadDisallowed.IsSet() {
			continue
		}
		heapSlice = append(heapSlice, heapElem{key, existingRequests[key], key.mightHavePartialFiles()})
	}
	aprioriHeap := heap.InterfaceForSlice(
		&heapSlice,
		func(l heapElem, r heapElem) bool {
			// Not stable ordering but being sticky to existing webseeds should be enough.
			return cmp.Or(
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
			) < 0
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
		plan.byCost[costKey] = append(plan.byCost[costKey], requestKey)
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

	for costKey, requestKeys := range plan.byCost {
		for _, requestKey := range requestKeys {
			// This could happen if a request is cancelled but hasn't removed itself from the active
			// list yet. This helps with backpressure as the requests can sleep to rate limit.
			if !cl.underWebSeedHttpRequestLimit(costKey) {
				break
			}
			if g.MapContains(existingRequests, requestKey) {
				continue
			}
			t := requestKey.t
			peer := t.webSeeds[requestKey.url]
			panicif.NotEq(peer.hostKey, costKey)
			printPlan()
			begin := requestKey.startRequest
			chunkEnd := t.endRequestForAlignedWebseedResponse(requestKey.startRequest)
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
			debugLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
				Level:     slog.LevelDebug,
				AddSource: true,
			})).With(
				"webseedUrl", requestKey.url,
				"webseedChunkIndex", requestKey.sliceIndex)
			// Request shouldn't exist if this occurs.
			panicif.LessThan(last, begin)
			// Hello darkness (C++) my old friend...
			end := last + 1
			end = min(end, t.endRequestForAlignedWebseedResponse(begin))
			panicif.LessThanOrEqual(end, begin)
			if webseed.PrintDebug && end != chunkEnd {
				debugLogger.Debug(
					"shortened webseed request",
					"request key", requestKey,
					"from", endExclusiveString(begin, chunkEnd),
					"to", endExclusiveString(begin, end))
			}
			panicif.GreaterThan(end, chunkEnd)
			peer.spawnRequest(begin, end, debugLogger)
		}
	}
}

// Cloudflare caches up to 512 MB responses by default. This is also an alignment.
var webseedRequestChunkSize uint64 = 256 << 20

func (t *Torrent) endRequestForAlignedWebseedResponse(start RequestIndex) RequestIndex {
	end := min(t.maxEndRequest(), nextMultiple(start, t.chunksPerAlignedWebseedResponse()))
	panicif.LessThanOrEqual(end, start)
	return end
}

func (t *Torrent) chunksPerAlignedWebseedResponse() RequestIndex {
	// This is the same as webseedRequestChunkSize, but in terms of RequestIndex.
	return RequestIndex(webseedRequestChunkSize / t.chunkSize.Uint64())
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
	byCost map[webseedHostKeyHandle][]webseedUniqueRequestKey
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
type aprioriWebseedRequestKey struct {
	url        webseedUrlKey
	t          *Torrent
	sliceIndex RequestIndex
}

func (me *aprioriWebseedRequestKey) String() string {
	return fmt.Sprintf("slice %v from %v", me.sliceIndex, me.url)
}

// Distinct webseed request when different offsets to the same object are allowed.
type webseedUniqueRequestKey struct {
	aprioriWebseedRequestKey
	startRequest RequestIndex
}

func (me webseedUniqueRequestKey) endPieceIndex() pieceIndex {
	return pieceIndex(intCeilDiv(
		me.t.endRequestForAlignedWebseedResponse(me.startRequest),
		me.t.chunksPerRegularPiece()))
}

func (me webseedUniqueRequestKey) mightHavePartialFiles() bool {
	return me.t.filesInPieceRangeMightBePartial(
		me.t.pieceIndexOfRequestIndex(me.startRequest),
		me.endPieceIndex())
}

func (me webseedUniqueRequestKey) longestFile() (ret g.Option[int64]) {
	t := me.t
	firstPiece := t.pieceIndexOfRequestIndex(me.startRequest)
	firstFileIndex := t.piece(firstPiece).beginFile
	endFileIndex := t.piece(me.endPieceIndex() - 1).endFile
	for fileIndex := firstFileIndex; fileIndex < endFileIndex; fileIndex++ {
		fileLength := t.getFile(fileIndex).length
		if ret.Ok {
			ret.Value = max(ret.Value, fileLength)
		} else {
			ret.Set(fileLength)
		}
	}
	return
}

func (me webseedUniqueRequestKey) String() string {
	return fmt.Sprintf(
		"%v at %v:%v",
		me.aprioriWebseedRequestKey,
		me.sliceIndex,
		me.startRequest%me.t.chunksPerAlignedWebseedResponse(),
	)
}

// Non-distinct proposed webseed request data.
type webseedRequestOrderValue struct {
	// The associated webseed request per host limit.
	costKey webseedHostKeyHandle
	// Used for cancellation if this is deprioritized. Also, a faster way to sort for existing
	// requests.
	existingWebseedRequest *webseedRequest
	priority               PiecePriority
}

func (me webseedRequestOrderValue) String() string {
	return fmt.Sprintf("%#v", me)
}

// Yields possible webseed requests by piece. Caller should filter and prioritize these.
func (cl *Client) iterPossibleWebseedRequests() iter.Seq2[webseedUniqueRequestKey, webseedRequestOrderValue] {
	return func(yield func(webseedUniqueRequestKey, webseedRequestOrderValue) bool) {
		for key, value := range cl.pieceRequestOrder {
			input := key.getRequestStrategyInput(cl)
			requestStrategy.GetRequestablePieces(
				input,
				value.pieces,
				func(ih metainfo.Hash, pieceIndex int, orderState requestStrategy.PieceRequestOrderState) bool {
					t := cl.torrentsByShortHash[ih]
					if len(t.webSeeds) == 0 {
						return false
					}
					p := t.piece(pieceIndex)
					cleanOpt := p.firstCleanChunk()
					if !cleanOpt.Ok {
						// Could almost return true here, as clearly something is going on with the piece.
						return false
					}
					// Pretty sure we want this and not the order state priority. That one is for
					// client piece request order and ignores other states like hashing, marking
					// etc. Order state priority would be faster otherwise.
					priority := p.effectivePriority()
					firstRequest := p.requestIndexBegin() + cleanOpt.Value
					webseedSliceIndex := firstRequest / t.chunksPerAlignedWebseedResponse()
					for url, ws := range t.webSeeds {
						// Return value from this function (RequestPieceFunc) doesn't terminate
						// iteration, so propagate that to not handling the yield return value.
						yield(
							webseedUniqueRequestKey{
								aprioriWebseedRequestKey{
									t:          t,
									sliceIndex: webseedSliceIndex,
									url:        url,
								},
								firstRequest,
							},
							webseedRequestOrderValue{
								priority: priority,
								costKey:  ws.hostKey,
							},
						)
					}
					// Pieces iterated here are only to select webseed requests. There's no guarantee they're chosen.
					return false
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
		for t := range cl.torrents {
			for url, ws := range t.webSeeds {
				for ar := range ws.activeRequests {
					if ar.next >= ar.end {
						// This request is done, so don't yield it.
						continue
					}
					p := t.piece(t.pieceIndexOfRequestIndex(ar.next))
					if !yield(
						webseedUniqueRequestKey{
							aprioriWebseedRequestKey{
								t:          t,
								sliceIndex: ar.next / t.chunksPerAlignedWebseedResponse(),
								url:        url,
							},
							ar.next,
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
