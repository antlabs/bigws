package greatws

import (
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
)

type MultiEventLoop struct {
	numLoops    int // 事件循环数量
	maxEventNum int
	loops       []*EventLoop
	t           task
	flag        evFlag // 是否使用io_uring
	level       slog.Level
	stat        // 统计信息
	*slog.Logger
}

func (m *MultiEventLoop) initDefaultSettingBefore() {
	m.level = slog.LevelError // 默认打印error级别的日志
	m.numLoops = 0
	m.maxEventNum = 10000
	m.t.min = 50
	m.t.initCount = 1000
	m.t.max = 30000
}

func (m *MultiEventLoop) initDefaultSettingAfter() {
	if m.numLoops == 0 {
		m.numLoops = runtime.NumCPU() / 4
		if m.numLoops == 0 {
			m.numLoops = 1
		}
	}

	if m.maxEventNum == 0 {
		m.maxEventNum = 256
	}

	if m.t.min == 0 {
		m.t.min = 50
	}

	if m.t.initCount == 0 {
		m.t.initCount = 1000
	}

	if m.t.max == 0 {
		m.t.max = 30000
	}

	if m.flag == 0 {
		m.flag = EVENT_EPOLL
	}
}

func NewMultiEventLoopMust(opts ...EvOption) *MultiEventLoop {
	m, err := NewMultiEventLoop(opts...)
	if err != nil {
		panic(err)
	}

	m.Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: m.level}))
	return m
}

// 创建一个多路事件循环
func NewMultiEventLoop(opts ...EvOption) (e *MultiEventLoop, err error) {
	m := &MultiEventLoop{}

	m.initDefaultSettingBefore()
	for _, o := range opts {
		o(m)
	}
	m.initDefaultSettingAfter()

	m.t.init()

	m.loops = make([]*EventLoop, m.numLoops)

	for i := 0; i < m.numLoops; i++ {
		m.loops[i], err = CreateEventLoop(m.maxEventNum, m.flag)
		if err != nil {
			return nil, err
		}
		m.loops[i].parent = m
	}
	return m, nil
}

// 启动多路事件循环
func (m *MultiEventLoop) Start() {
	for _, loop := range m.loops {
		go loop.Loop()
	}
}

// 添加一个连接到多路事件循环
func (m *MultiEventLoop) add(c *Conn) error {
	fd := c.getFd()
	index := fd % len(m.loops)
	m.loops[index].conns.Store(fd, c)
	if err := m.loops[index].addRead(c); err != nil {
		m.del(c)
		return err
	}
	c.setParent(m.loops[index])
	atomic.AddInt64(&m.curConn, 1)
	return nil
}

// 添加一个可写事件到多路事件循环
func (m *MultiEventLoop) addWrite(c *Conn) error {
	fd := c.getFd()
	if fd == -1 {
		return nil
	}
	index := fd % len(m.loops)
	if err := m.loops[index].addWrite(c); err != nil {
		return err
	}
	m.loops[index].conns.LoadOrStore(fd, c)
	return nil
}

// 添加一个可写事件到多路事件循环
func (m *MultiEventLoop) delWrite(c *Conn) error {
	fd := c.getFd()
	if fd == -1 {
		return nil
	}

	index := fd % len(m.loops)
	if err := m.loops[index].delWrite(c); err != nil {
		return err
	}
	m.loops[index].conns.LoadOrStore(fd, c)
	return nil
}

// 从多路事件循环中删除一个连接
func (m *MultiEventLoop) del(c *Conn) {
	fd := c.getFd()

	if fd == -1 {
		return
	}
	atomic.AddInt64(&m.curConn, -1)
	index := fd % len(m.loops)
	m.loops[index].conns.Delete(fd)
	closeFd(fd)
}

// 获取一个连接
func (m *MultiEventLoop) getConn(fd int) *Conn {
	index := fd % len(m.loops)
	v, ok := m.loops[index].conns.Load(fd)
	if !ok {
		return nil
	}
	return v.(*Conn)
}
