package greatws

import (
	"sync"
)

var (
	getPaylodNum     int64
	putPayloadNum    int64
	getBigPayloadNum int64
	putBigPayloadNum int64
)

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

	// fmt.Printf("get payload bytes: %d\n", n)
	index := getSelectIndex(n)
	if index >= len(pools) {
		return getBigPayload(n)
	}

	// 最多尝试3次
	for i := 0; i < 3; i++ {

		// 可能是不规则的大小
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

// 可以保存不规则大小的payload
func PutPayloadBytes(bytes *[]byte) {
	// fmt.Printf("put payload bytes: %d:%d\n", len(*bytes), cap(*bytes))

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
