---
name: golang-dsa
description: >
  Go-optimized data structures and algorithms: Big-O analysis, cache-friendly layouts,
  generics-based collections (stack, queue, heap, trie, skiplist, bloom filter, LRU, ring buffer),
  sorting, searching, graph algorithms (BFS, DFS, Dijkstra, topological sort), dynamic programming,
  and memory-efficient designs. Use when implementing custom data structures, optimizing collection
  access patterns, designing cache or index structures, or solving algorithmic problems in production.
---

# Go DSA — Production-Grade Data Structures & Algorithms

## 1. Complexity Reference

```
Data Structure        Access  Search  Insert  Delete  Space
─────────────────────────────────────────────────────────────
Array / Slice         O(1)    O(n)    O(n)    O(n)    O(n)
Hash Map (Go map)     O(1)    O(1)    O(1)    O(1)    O(n)
Sorted Slice + bin    O(1)    O(lgn)  O(n)    O(n)    O(n)
BST (balanced)        O(lgn)  O(lgn)  O(lgn)  O(lgn)  O(n)
Heap (binary)         O(1)p   O(n)    O(lgn)  O(lgn)  O(n)
Trie                  O(k)    O(k)    O(k)    O(k)    O(n*k)
Skip List             O(lgn)  O(lgn)  O(lgn)  O(lgn)  O(n lgn)
Bloom Filter          —       O(k)    O(k)    —       O(m)

Cache-friendliness: slices > linked lists >> trees >> hash maps (with pointer chasing)
```

---

## 2. Generic Stack

```go
type Stack[T any] struct {
    items []T
}

func (s *Stack[T]) Push(v T)       { s.items = append(s.items, v) }
func (s *Stack[T]) Len() int       { return len(s.items) }
func (s *Stack[T]) IsEmpty() bool  { return len(s.items) == 0 }

func (s *Stack[T]) Pop() (T, bool) {
    var zero T
    if len(s.items) == 0 { return zero, false }
    top := s.items[len(s.items)-1]
    s.items[len(s.items)-1] = zero // zero for GC
    s.items = s.items[:len(s.items)-1]
    return top, true
}

func (s *Stack[T]) Peek() (T, bool) {
    var zero T
    if len(s.items) == 0 { return zero, false }
    return s.items[len(s.items)-1], true
}
```

---

## 3. Generic Min/Max Heap

```go
import "container/heap"

// MinHeap[T] implements a generic min-heap backed by container/heap
type MinHeap[T any] struct {
    data []T
    less func(a, b T) bool
}

func NewMinHeap[T any](less func(a, b T) bool, initial []T) *MinHeap[T] {
    h := &MinHeap[T]{data: append([]T(nil), initial...), less: less}
    heap.Init(h)
    return h
}

func (h *MinHeap[T]) Len() int            { return len(h.data) }
func (h *MinHeap[T]) Less(i, j int) bool  { return h.less(h.data[i], h.data[j]) }
func (h *MinHeap[T]) Swap(i, j int)       { h.data[i], h.data[j] = h.data[j], h.data[i] }
func (h *MinHeap[T]) Push(x any)          { h.data = append(h.data, x.(T)) }
func (h *MinHeap[T]) Pop() any {
    n := len(h.data)
    v := h.data[n-1]
    var zero T; h.data[n-1] = zero // GC
    h.data = h.data[:n-1]
    return v
}

// Public interface
func (h *MinHeap[T]) Insert(v T)       { heap.Push(h, v) }
func (h *MinHeap[T]) ExtractMin() T    { return heap.Pop(h).(T) }
func (h *MinHeap[T]) Peek() T          { return h.data[0] }
```

---

## 4. LRU Cache (O(1) get/put)

```go
// LRU using doubly-linked list + hash map
type LRU[K comparable, V any] struct {
    cap   int
    mu    sync.Mutex
    items map[K]*lruNode[K, V]
    head  *lruNode[K, V] // most recent
    tail  *lruNode[K, V] // least recent
}

type lruNode[K, V any] struct {
    key        K
    val        V
    prev, next *lruNode[K, V]
}

func NewLRU[K comparable, V any](capacity int) *LRU[K, V] {
    if capacity <= 0 { panic("LRU: capacity must be positive") }
    head := &lruNode[K, V]{}
    tail := &lruNode[K, V]{}
    head.next = tail; tail.prev = head
    return &LRU[K, V]{cap: capacity, items: make(map[K]*lruNode[K, V], capacity), head: head, tail: tail}
}

func (c *LRU[K, V]) Get(key K) (V, bool) {
    c.mu.Lock(); defer c.mu.Unlock()
    if n, ok := c.items[key]; ok {
        c.moveToFront(n)
        return n.val, true
    }
    var zero V; return zero, false
}

func (c *LRU[K, V]) Put(key K, val V) {
    c.mu.Lock(); defer c.mu.Unlock()
    if n, ok := c.items[key]; ok {
        n.val = val; c.moveToFront(n); return
    }
    n := &lruNode[K, V]{key: key, val: val}
    c.items[key] = n; c.insertFront(n)
    if len(c.items) > c.cap { c.evict() }
}

func (c *LRU[K, V]) evict() {
    lru := c.tail.prev
    c.remove(lru); delete(c.items, lru.key)
}

func (c *LRU[K, V]) moveToFront(n *lruNode[K, V]) { c.remove(n); c.insertFront(n) }

func (c *LRU[K, V]) insertFront(n *lruNode[K, V]) {
    n.next = c.head.next; n.prev = c.head
    c.head.next.prev = n; c.head.next = n
}

func (c *LRU[K, V]) remove(n *lruNode[K, V]) {
    n.prev.next = n.next; n.next.prev = n.prev
}

func (c *LRU[K, V]) Len() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.items) }
```

---

## 5. Ring Buffer (Lock-Free SPSC)

```go
// Single-producer, single-consumer lock-free ring buffer (power-of-2 size)
type RingBuffer[T any] struct {
    buf  []T
    mask uint64
    head uint64 // written by producer
    _    [56]byte // padding to separate cache lines
    tail uint64 // written by consumer
    _    [56]byte
}

func NewRingBuffer[T any](size uint64) *RingBuffer[T] {
    if size == 0 || size&(size-1) != 0 {
        panic("RingBuffer: size must be a power of 2")
    }
    return &RingBuffer[T]{buf: make([]T, size), mask: size - 1}
}

// Offer adds item. Returns false if buffer is full (non-blocking).
func (r *RingBuffer[T]) Offer(v T) bool {
    head := atomic.LoadUint64(&r.head)
    tail := atomic.LoadUint64(&r.tail)
    if head-tail >= uint64(len(r.buf)) { return false } // full
    r.buf[head&r.mask] = v
    atomic.StoreUint64(&r.head, head+1)
    return true
}

// Poll removes and returns item. Returns false if empty (non-blocking).
func (r *RingBuffer[T]) Poll() (T, bool) {
    var zero T
    tail := atomic.LoadUint64(&r.tail)
    head := atomic.LoadUint64(&r.head)
    if tail == head { return zero, false } // empty
    v := r.buf[tail&r.mask]
    r.buf[tail&r.mask] = zero // zero for GC
    atomic.StoreUint64(&r.tail, tail+1)
    return v, true
}

func (r *RingBuffer[T]) Len() int {
    h := atomic.LoadUint64(&r.head)
    t := atomic.LoadUint64(&r.tail)
    return int(h - t)
}
```

---

## 6. Bloom Filter

```go
// Space-efficient probabilistic membership test. False positives possible, never false negatives.
type BloomFilter struct {
    bits    []uint64
    numBits uint64
    numHash uint
}

func NewBloomFilter(n uint64, falsePositiveRate float64) *BloomFilter {
    // m = -n*ln(p) / (ln(2)^2)
    m := uint64(-float64(n) * math.Log(falsePositiveRate) / (math.Log(2) * math.Log(2)))
    m = (m + 63) &^ 63 // round up to multiple of 64
    k := uint(math.Round(float64(m) / float64(n) * math.Log(2)))
    if k < 1 { k = 1 }
    return &BloomFilter{bits: make([]uint64, m/64), numBits: m, numHash: k}
}

func (f *BloomFilter) Add(data []byte) {
    h1, h2 := xxhash(data), fnvhash(data)
    for i := uint(0); i < f.numHash; i++ {
        bit := (h1 + uint64(i)*h2) % f.numBits
        f.bits[bit/64] |= 1 << (bit % 64)
    }
}

func (f *BloomFilter) MightContain(data []byte) bool {
    h1, h2 := xxhash(data), fnvhash(data)
    for i := uint(0); i < f.numHash; i++ {
        bit := (h1 + uint64(i)*h2) % f.numBits
        if f.bits[bit/64]&(1<<(bit%64)) == 0 { return false }
    }
    return true
}

func xxhash(data []byte) uint64 {
    h := fnv.New64a(); h.Write(data); return h.Sum64()
}
func fnvhash(data []byte) uint64 {
    h := fnv.New64(); h.Write(data); return h.Sum64()
}
```

---

## 7. Binary Search (Generic)

```go
// BinarySearch returns index of target in sorted slice, or -(insertion point)-1
func BinarySearch[T constraints.Ordered](sorted []T, target T) int {
    lo, hi := 0, len(sorted)-1
    for lo <= hi {
        mid := lo + (hi-lo)/2 // avoids overflow vs (lo+hi)/2
        switch {
        case sorted[mid] == target: return mid
        case sorted[mid] < target:  lo = mid + 1
        default:                    hi = mid - 1
        }
    }
    return -(lo + 1) // not found: return insertion point encoded
}

// LowerBound: first index where sorted[i] >= target
func LowerBound[T constraints.Ordered](sorted []T, target T) int {
    return sort.Search(len(sorted), func(i int) bool { return sorted[i] >= target })
}
```

---

## 8. Graph Algorithms

```go
// Directed graph with adjacency list
type Graph[T comparable] struct {
    adj  map[T][]T
    edge map[[2]T]int // edge weights
}

func NewGraph[T comparable]() *Graph[T] {
    return &Graph[T]{adj: make(map[T][]T), edge: make(map[[2]T]int)}
}

func (g *Graph[T]) AddEdge(from, to T, weight int) {
    g.adj[from] = append(g.adj[from], to)
    g.edge[[2]T{from, to}] = weight
}

// BFS — shortest path in unweighted graph
func (g *Graph[T]) BFS(start T) []T {
    visited := map[T]bool{start: true}
    queue := []T{start}
    order := []T{}
    for len(queue) > 0 {
        node := queue[0]; queue = queue[1:]
        order = append(order, node)
        for _, neighbor := range g.adj[node] {
            if !visited[neighbor] {
                visited[neighbor] = true
                queue = append(queue, neighbor)
            }
        }
    }
    return order
}

// Dijkstra — shortest path in weighted graph
func (g *Graph[T]) Dijkstra(start T) map[T]int {
    dist := map[T]int{start: 0}
    pq := NewMinHeap(func(a, b [2]any) bool {
        return a[0].(int) < b[0].(int)
    }, nil)
    pq.Insert([2]any{0, start})

    for pq.Len() > 0 {
        top := pq.ExtractMin()
        d, node := top[0].(int), top[1].(T)
        if d > dist[node] { continue } // stale
        for _, neighbor := range g.adj[node] {
            nd := d + g.edge[[2]T{node, neighbor}]
            if cur, ok := dist[neighbor]; !ok || nd < cur {
                dist[neighbor] = nd
                pq.Insert([2]any{nd, neighbor})
            }
        }
    }
    return dist
}

// Topological sort (Kahn's algorithm — iterative, detects cycles)
func (g *Graph[T]) TopologicalSort() ([]T, error) {
    inDegree := map[T]int{}
    for node := range g.adj { inDegree[node] = inDegree[node] }
    for _, neighbors := range g.adj {
        for _, n := range neighbors { inDegree[n]++ }
    }
    queue := []T{}
    for node, deg := range inDegree {
        if deg == 0 { queue = append(queue, node) }
    }
    result := []T{}
    for len(queue) > 0 {
        node := queue[0]; queue = queue[1:]
        result = append(result, node)
        for _, neighbor := range g.adj[node] {
            inDegree[neighbor]--
            if inDegree[neighbor] == 0 { queue = append(queue, neighbor) }
        }
    }
    if len(result) != len(inDegree) {
        return nil, errors.New("graph contains a cycle")
    }
    return result, nil
}
```

---

## DSA Selection Guide

| Use case | Structure | Why |
|---|---|---|
| LIFO undo/history | Stack[T] | O(1) push/pop |
| FIFO task queue | RingBuffer[T] | Cache-friendly, lock-free |
| Priority scheduling | MinHeap[T] | O(lg n) insert/extract |
| Fixed-size cache | LRU[K,V] | O(1) get/put with eviction |
| Membership test (approximate) | BloomFilter | O(k) space-efficient |
| Prefix search / autocomplete | Trie | O(k) per operation |
| Deduplication at scale | BloomFilter | Space vs exactness tradeoff |
| Sorted range queries | sorted slice + BinarySearch | Cache-friendly vs BST |
| Task dependency ordering | Topological sort | Detects cycles |
| Shortest path | Dijkstra | Weighted; BFS for unweighted |
