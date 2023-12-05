package greatws

import (
	"sync/atomic"
	"time"
)

var exitFunc = func() bool { return true }

type task struct {
	c chan func() bool

	initCount int   // 初始化的协程数
	min       int   // 最小协程数
	max       int   // 最大协程数
	curGo     int64 // 当前运行协程数
	curTask   int64 // 当前运行任务数
}

func (t *task) init() {
	t.c = make(chan func() bool)
	go t.manageGo()
	go t.runConsumerLoop()
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
	for f := range t.c {
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

		// 这里的判断条件不准确，因为curGO是表示go程多少，不能表示任务多少, 比如1w上go程，一个任务也不跑
		// if curGo := atomic.LoadInt64(&t.curGo); curGo > int64(t.max) {
		// 	return true
		// }

		if !t.needResize() {
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
// 在io线程里面会判断go程池是否高负载，如果是高负载，会取消read的任务, 放到overflow里面, 延后再处理
func (t *task) addTask(f func() bool) {
	for {
		select {
		case t.c <- f:
			return
		default:
			t.addGo()
		}
	}
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

func (t *task) needResize() bool {
	if int(t.curGo) > t.max {
		return false
	}

	curTask := atomic.LoadInt64(&t.curTask)
	curGo := atomic.LoadInt64(&t.curGo)
	return (float64(curTask) / float64(curGo)) > 0.8
}

// 管理go程
func (t *task) manageGo() {
	for {

		time.Sleep(time.Second * 5)
		curTask := atomic.LoadInt64(&t.curTask)
		curGo := atomic.LoadInt64(&t.curGo)

		if curTask < int64(t.min) && curGo > int64(t.min) {
			t.cancelGo()
		} else if t.needResize() {
			t.addGo()
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
