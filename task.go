// Copyright 2021-2024 antlabs. All rights reserved.
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
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// 运行task的策略
// 1. 随机
// 2. 取余映射
type taskStrategy int

const (
	// 随机
	taskStrategyRandom taskStrategy = iota
	// 取余映射
	taskStrategyMod
	// 流式映射
	taskStrategyStream
)

var ErrTaskQueueFull = errors.New("task queue full")

var exitFunc = func() bool { return true }

type taskConfig struct {
	initCount int // 初始化的协程数
	min       int // 最小协程数
	max       int // 最大协程数
}

type task struct {
	c       chan func() bool
	windows windows // 滑动窗口计数，用于判断是否需要新增go程
	taskConfig
	curGo   int64 // 当前运行协程数
	curTask int64 // 当前运行任务数

	allMu         sync.Locker
	allBusinessGo []*businessGo
}

// 无锁
type noMutex struct{}

func (noMutex) Lock()   {}
func (noMutex) Unlock() {}

// 初始化
func (t *task) initInner() {
	t.c = make(chan func() bool)
	t.windows.init()
	go t.manageGo()
	go t.runConsumerLoop()
}

func (t *task) init() {
	t.initInner()
	t.allMu = &sync.Mutex{}
}

func (t *task) initWithNoMutex() {
	t.initInner()
	t.allMu = noMutex{}
}

func (t *task) getCurGo() int64 {
	return atomic.LoadInt64(&t.curGo)
}

func (t *task) getCurTask() int64 {
	return atomic.LoadInt64(&t.curTask)
}

// 消费者循环
func (t *task) consumer() {
	defer atomic.AddInt64(&t.curGo, -1)
	currBusinessGo := newBusinessGo()
	t.allMu.Lock()
	t.allBusinessGo = append(t.allBusinessGo, currBusinessGo)
	t.allMu.Unlock()

	var f func() bool
	for {
		select {
		case f = <-t.c:
		case f = <-currBusinessGo.taskChan:
		}
		atomic.AddInt64(&t.curTask, 1)
		if b := f(); b {
			atomic.AddInt64(&t.curTask, -1)
			break
		}
		atomic.AddInt64(&t.curTask, -1)
	}
}

func (t *task) highLoad() bool {
	// curGo := atomic.LoadInt64(&t.curGo)
	for i := 1; ; i++ {

		curTask := atomic.LoadInt64(&t.curTask)

		if curTask >= int64(t.curGo) {
			return true
		}

		// 这里的判断条件不准确，因为curGo是表示go程多少，不能表示任务多少, 比如1w上go程，一个任务也不跑
		// if curGo := atomic.LoadInt64(&t.curGo); curGo > int64(t.max) {
		// 	return true
		// }

		if need, _ := t.needResize(); !need {
			return false
		}

		curGo := atomic.LoadInt64(&t.curGo)
		maxGo := int64(t.max)
		need := min(2*i, max(0, int(maxGo-curGo)))
		if need > 0 {
			t.addGoNum(need)
		}
	}
}

// 新增任务, 如果任务队列满了, 新增go程， 这可能会导致协程数超过最大值, 为了防止死锁，还是需要新增业务go程
// 在io线程里面会判断go程池是否高负载，如果是高负载，会取消read的任务, 放到wbuf里面, 延后再处理
func (t *task) addTask(fd int, ts taskStrategy, f func() bool) error {
	if fd == -1 {
		return nil
	}

	if ts == taskStrategyMod {
		t.allMu.Lock()
		size := len(t.allBusinessGo)
		currChan := t.allBusinessGo[fd%size]
		// 如果任务未满，直接放入任务队列
		if len(currChan.taskChan) < cap(currChan.taskChan) {
			t.allMu.Unlock()
			currChan.taskChan <- f
			return nil
		}
		t.allMu.Unlock()
	}

	if len(t.c) >= cap(t.c) {
		return ErrTaskQueueFull
	}
	t.c <- f
	return nil
}

// 新增go程
func (t *task) addGo() {
	go func() {
		atomic.AddInt64(&t.curGo, 1)
		defer atomic.AddInt64(&t.curGo, -1)
		t.consumer()
	}()
}

func (t *task) addGoNum(n int) {
	for i := 0; i < n; i++ {
		t.addGo()
	}
}

// 取消go程
func (t *task) cancelGo() {
	if atomic.LoadInt64(&t.curGo) > int64(t.min) {
		t.c <- exitFunc
	}
}

func (t *task) needResize() (bool, int) {
	if int(t.curGo) > t.max {
		return false, 0
	}

	curTask := atomic.LoadInt64(&t.curTask)
	curGo := atomic.LoadInt64(&t.curGo)
	need := (float64(curTask)/float64(curGo)) > 0.8 && curGo > int64(t.windows.avg())

	if need {
		avg := t.windows.avg()
		if avg*2 < 8 {
			return true, 16
		}

		if avg*2 < 1024 {
			return true, int(t.windows.avg() * 2)
		}

		return true, int(float64(t.curGo) * 1.25)
	}

	return false, 0
}

// 管理go程
func (t *task) manageGo() {

	for {
		time.Sleep(time.Second * 1)
		curTask := atomic.LoadInt64(&t.curTask)
		curGo := atomic.LoadInt64(&t.curGo)
		t.windows.add(curGo)
		if curTask < int64(t.min) && curGo > int64(t.min) {
			t.cancelGo()
		} else if need, newSize := t.needResize(); need {
			t.addGoNum(newSize - int(curGo))
		}
	}

}

// 运行任务
func (t *task) runConsumerLoop() {
	atomic.AddInt64(&t.curGo, int64(t.initCount))
	for i := 0; i < t.initCount; i++ {
		go t.consumer()
	}
}
