package dht

import (
	"container/heap"
)

type nodeMaxHeap struct {
	IDs    []string
	Target string
}

func (me nodeMaxHeap) Len() int { return len(me.IDs) }

func (me nodeMaxHeap) Less(i, j int) bool {
	return idDistance(me.IDs[i], me.Target) > idDistance(me.IDs[j], me.Target)
}

func (me *nodeMaxHeap) Pop() (ret interface{}) {
	ret, me.IDs = me.IDs[len(me.IDs)-1], me.IDs[:len(me.IDs)-1]
	return
}
func (me *nodeMaxHeap) Push(val interface{}) {
	me.IDs = append(me.IDs, val.(string))
}
func (me nodeMaxHeap) Swap(i, j int) {
	me.IDs[i], me.IDs[j] = me.IDs[i], me.IDs[j]
}

type closestNodesSelector struct {
	closest nodeMaxHeap
	k       int
}

func (me *closestNodesSelector) Push(id string) {
	heap.Push(&me.closest, id)
	if me.closest.Len() > me.k {
		heap.Pop(&me.closest)
	}
}

func (me *closestNodesSelector) IDs() []string {
	return me.closest.IDs
}

func newKClosestNodesSelector(k int, targetID string) (ret closestNodesSelector) {
	ret.k = k
	ret.closest.Target = targetID
	return
}
