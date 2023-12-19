package greatws

import (
	"sync"
)

// 多级缓存池，只有当缓存池的大小不够时，才会使用大缓存池

// 多级缓存池，主要是为了减少内存占用
// greatws 主要有三部分内存占用:
//  1. read buffer(没有使用本缓存池)
//     1.1 直接调用unix.Read时使用(没有使用本缓存池)
//     1.2 websocket payload数据包(使用)
//  2. write buffer
//     2.1  直接调用unix.Write时使用(没有使用本缓存池)
//     2.2  unix.Write失败调用，缓存未成功写入数据, c.wbuf(使用)
//  3. fragment buffer websocket协议里面的分段数据包(使用)
func init() {
	for i := 1; i <= maxIndex; i++ {
		j := i
		pools = append(pools, sync.Pool{
			New: func() interface{} {
				buf := make([]byte, j*pageSize)
				return &buf
			},
		})
	}
}

const (
	pageSize = 1024
	maxIndex = 128
)

var (
	pools      = make([]sync.Pool, 0, maxIndex)
	emptyBytes = make([]byte, 0)
)

func getSelectIndex(n int) int {
	n--
	if n < pageSize {
		return 0
	}

	index := n / pageSize
	return index
}

func putSelectIndex(n int) int {
	return getSelectIndex(n)
}

func GetPayloadBytes(n int) (rv *[]byte) {
	if n == 0 {
		return &emptyBytes
	}

	index := getSelectIndex(n)
	if index >= len(pools) {
		return getBigPayload(n)
	}

	for i := 0; i < 3; i++ {

		rv2 := pools[index].Get().(*[]byte)
		if cap(*rv2) < n {
			continue
		}
		*rv2 = (*rv2)[:n]
		return rv2
	}

	rv2 := make([]byte, (index+1)*pageSize)
	rv2 = rv2[:n]
	return &rv2
}

func PutPayloadBytes(bytes *[]byte) {
	if cap(*bytes) == 0 {
		return
	}

	*bytes = (*bytes)[:cap(*bytes)]

	newLen := cap(*bytes)
	index := putSelectIndex(newLen)
	if index >= len(pools) {
		putBigPayload(bytes)
		return
	}
	pools[index].Put(bytes)
}
