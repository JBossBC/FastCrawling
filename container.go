package main

type FItem struct {
	value    string
	priority int
	index    int
}

type PriorityQueue []*FItem

func (pq PriorityQueue) Len() int {
	return len(pq)
}

func (pq PriorityQueue) Less(i, j int) bool {
	return pq[i].priority > pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x any) {
	item := x.(*FItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.index = -1 // 从堆中移除
	*pq = old[0 : n-1]
	return item
}

// func (pq PriorityQueue) Max() *FItem {
// 	return pq[0]
// }

// func (pq PriorityQueue) Min() *FItem {
//      return pq[len(pq)]
// }
