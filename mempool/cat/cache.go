package cat

import (
	"container/list"
	"time"

	tmsync "github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/types"
)

// LRUTxCache maintains a thread-safe LRU cache of raw transactions. The cache
// only stores the hash of the raw transaction.
// NOTE: This has been copied from mempool/cache with the main diffence of using
// tx keys instead of raw transactions.
type LRUTxCache struct {
	staticSize int

	mtx      tmsync.Mutex
	cacheMap map[types.TxKey]*list.Element
	list     *list.List
}

func NewLRUTxCache(cacheSize int) *LRUTxCache {
	return &LRUTxCache{
		staticSize: cacheSize,
		cacheMap:   make(map[types.TxKey]*list.Element, cacheSize),
		list:       list.New(),
	}
}

func (c *LRUTxCache) Reset() {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.cacheMap = make(map[types.TxKey]*list.Element, c.staticSize)
	c.list.Init()
}

func (c *LRUTxCache) Push(txKey types.TxKey) bool {
	if c.staticSize == 0 {
		return true
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	moved, ok := c.cacheMap[txKey]
	if ok {
		c.list.MoveToBack(moved)
		return false
	}

	if c.list.Len() >= c.staticSize {
		front := c.list.Front()
		if front != nil {
			frontKey := front.Value.(types.TxKey)
			delete(c.cacheMap, frontKey)
			c.list.Remove(front)
		}
	}

	e := c.list.PushBack(txKey)
	c.cacheMap[txKey] = e

	return true
}

func (c *LRUTxCache) Remove(txKey types.TxKey) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	e := c.cacheMap[txKey]
	delete(c.cacheMap, txKey)

	if e != nil {
		c.list.Remove(e)
	}
}

func (c *LRUTxCache) Has(txKey types.TxKey) bool {
	if c.staticSize == 0 {
		return false
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[txKey]
	return ok
}

type EvictedTxInfo struct {
	timeEvicted time.Time
	priority    int64
	gasWanted   int64
	sender      string
	size        int64
}

type EvictedTxCache struct {
	staticSize int

	mtx   tmsync.Mutex
	cache map[types.TxKey]*EvictedTxInfo
}

func NewEvictedTxCache(size int) *EvictedTxCache {
	return &EvictedTxCache{
		staticSize: size,
		cache:      make(map[types.TxKey]*EvictedTxInfo),
	}
}

func (c *EvictedTxCache) Has(txKey types.TxKey) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	_, exists := c.cache[txKey]
	return exists
}

func (c *EvictedTxCache) Get(txKey types.TxKey) *EvictedTxInfo {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return c.cache[txKey]
}

func (c *EvictedTxCache) Push(wtx *wrappedTx) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.cache[wtx.key] = &EvictedTxInfo{
		timeEvicted: time.Now().UTC(),
		priority:    wtx.priority,
		gasWanted:   wtx.gasWanted,
		sender:      wtx.sender,
		size:        wtx.size(),
	}
	// if cache too large, remove the oldest entry
	if len(c.cache) > c.staticSize {
		oldestTxKey := wtx.key
		oldestTxTime := time.Now().UTC()
		for key, info := range c.cache {
			if info.timeEvicted.Before(oldestTxTime) {
				oldestTxTime = info.timeEvicted
				oldestTxKey = key
			}
		}
		delete(c.cache, oldestTxKey)
	}
}

func (c *EvictedTxCache) Pop(txKey types.TxKey) *EvictedTxInfo {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	info, exists := c.cache[txKey]
	if !exists {
		return nil
	}
	delete(c.cache, txKey)
	return info
}

func (c *EvictedTxCache) Prune(limit time.Time) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for key, info := range c.cache {
		if info.timeEvicted.Before(limit) {
			delete(c.cache, key)
		}
	}
}

func (c *EvictedTxCache) Reset() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.cache = make(map[types.TxKey]*EvictedTxInfo)
}

// SeenTxSet records transactions that have been
// seen by other peers but not yet by us
type SeenTxSet struct {
	mtx tmsync.Mutex
	set map[types.TxKey]timestampedPeerSet
}

type timestampedPeerSet struct {
	peers map[uint16]bool
	time  time.Time
}

func NewSeenTxSet() *SeenTxSet {
	return &SeenTxSet{
		set: make(map[types.TxKey]timestampedPeerSet),
	}
}

func (s *SeenTxSet) Add(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		s.set[txKey] = timestampedPeerSet{
			peers: map[uint16]bool{peer: true},
			time:  time.Now().UTC(),
		}
	} else {
		seenSet.peers[peer] = true
	}
}

func (s *SeenTxSet) Pop(txKey types.TxKey) uint16 {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if exists {
		for peer := range seenSet.peers {
			delete(seenSet.peers, peer)
			return peer
		}
	}
	return 0
}

func (s *SeenTxSet) RemoveKey(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.set, txKey)
}

func (s *SeenTxSet) Remove(txKey types.TxKey, peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	set, exists := s.set[txKey]
	if exists {
		if len(set.peers) == 1 {
			delete(s.set, txKey)
		} else {
			delete(set.peers, peer)
		}
	}
}

func (s *SeenTxSet) Prune(limit time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		if seenSet.time.Before(limit) {
			delete(s.set, key)
		}
	}
}

func (s *SeenTxSet) Has(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return false
	}
	return seenSet.peers[peer]
}

func (s *SeenTxSet) Get(txKey types.TxKey) map[uint16]struct{} {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return nil
	}
	// make a copy of the struct to avoid concurrency issues
	peers := make(map[uint16]struct{}, len(seenSet.peers))
	for peer := range seenSet.peers {
		peers[peer] = struct{}{}
	}
	return peers
}

// Len returns the amount of cached items. Mostly used for testing.
func (s *SeenTxSet) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.set)
}

func (s *SeenTxSet) Reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.set = make(map[types.TxKey]timestampedPeerSet)
}