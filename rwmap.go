package greatws

import "sync"

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

type RWMap[K comparable, V any] struct {
	rw sync.RWMutex
	m  map[K]V
}

// 通过new函数分配可以指定map的长度
func New[K comparable, V any](l int) *RWMap[K, V] {
	return &RWMap[K, V]{
		m: make(map[K]V, l),
	}
}

// 删除
func (r *RWMap[K, V]) Delete(key K) {
	r.rw.Lock()
	delete(r.m, key)
	r.rw.Unlock()
}

func (r *RWMap[K, V]) RangeDelete(key K) {
	delete(r.m, key)
}

// 加载
func (r *RWMap[K, V]) Load(key K) (value V, ok bool) {
	r.rw.RLock()
	value, ok = r.m[key]
	r.rw.RUnlock()
	return
}

func (r *RWMap[K, V]) Store(key K, value V) {
	r.rw.Lock()
	if r.m == nil {
		r.m = make(map[K]V)
	}
	r.m[key] = value
	r.rw.Unlock()
}

func (r *RWMap[K, V]) RangeSafe(f func(key K, value V) bool) {
	r.rw.Lock()
	for k, v := range r.m {
		if !f(k, v) {
			break
		}
	}
	r.rw.Unlock()
}

// 返回长度
func (r *RWMap[K, V]) Len() (l int) {
	r.rw.RLock()
	l = len(r.m)
	r.rw.RUnlock()
	return
}
