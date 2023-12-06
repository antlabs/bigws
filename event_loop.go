// Copyright 2021-2023 antlabs. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package greatws

import (
	"context"
	"sync"
	"time"
)

type evFlag int

const (
	EVENT_EPOLL evFlag = 1 << iota
	EVENT_IOURING
	defaultWriteTimeout = 250 * time.Millisecond
)

type EventLoop struct {
	mu               sync.Mutex
	conns            sync.Map
	overflowRead     *RWMap[int, *Conn]
	overflowWrite    *RWMap[int, *Conn]
	prevOverFlowRead int
	shutdown         bool
	*apiState        // 每个平台对应的异步io接口/epoll/kqueue/iouring
	parent           *MultiEventLoop
}

// 初始化函数
func CreateEventLoop(size int, flag evFlag) (e *EventLoop, err error) {
	e = &EventLoop{}
	e.overflowRead = New[int, *Conn](512)
	e.overflowWrite = New[int, *Conn](512)
	err = e.apiCreate(size, flag)
	return e, err
}

// 柔性关闭所有的连接
func (e *EventLoop) Shutdown(ctx context.Context) error {
	return nil
}

func (el *EventLoop) StartLoop() {
	go el.Loop()
}

func (el *EventLoop) Loop() {
	index := 1
	maxIndex := 30
	base := time.Millisecond * 250

	for !el.shutdown {
		_, err := el.apiPoll(time.Duration(int(base) * index))
		if err != nil {
			el.parent.Error("apiPolll", "err", err.Error())
			return
		}

		if el.prevOverFlowRead < el.overflowRead.Len() {
			index++
		} else {
			index--
		}

		if index < 0 {
			index = 1
		} else if index > maxIndex {
			index = maxIndex
		}
		el.prevOverFlowRead = el.overflowRead.Len()
	}
}

func (el *EventLoop) GetApiName() string {
	return el.apiName()
}
