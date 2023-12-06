//go:build linux || darwin || netbsd || freebsd || openbsd || dragonfly
// +build linux darwin netbsd freebsd openbsd dragonfly

package greatws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/antlabs/wsutil/bytespool"
	"golang.org/x/sys/unix"
)

type ioUringOpState uint32

const (
	connInvalid ioUringOpState = 1 << iota
	opRead
	opWrite
	opClose
)

func (s ioUringOpState) String() string {
	switch s {
	case opRead:
		return "read"
	case opWrite:
		return "write"
	case opClose:
		return "close"
	default:
		return "invalid"
	}
}

type Conn struct {
	conn

	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	canBeWrite       chan struct{}
	client           bool  // 客户端为true，服务端为false
	closed           int32 // 是否关闭
	waitOnMessageRun sync.WaitGroup
	closeOnce        sync.Once
	parent           *EventLoop
	nwrite           int32
	*Config          // 配置
}

func (c *Conn) setParent(el *EventLoop) {
	atomic.StorePointer((*unsafe.Pointer)((unsafe.Pointer)(&c.parent)), unsafe.Pointer(el))
}

func (c *Conn) getParent() *EventLoop {
	return (*EventLoop)(atomic.LoadPointer((*unsafe.Pointer)((unsafe.Pointer)(&c.parent))))
}

func newConn(fd int64, client bool, conf *Config) *Conn {
	rbuf := bytespool.GetBytes(conf.initPayloadSize())
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		conn: conn{
			fd:   fd,
			rbuf: rbuf,
		},
		// 初始化不分配内存，只有在需要的时候才分配
		Config:     conf,
		client:     client,
		canBeWrite: make(chan struct{}, 1),
		ctx:        ctx,
		cancel:     cancel,
	}

	return c
}

func duplicateSocket(socketFD int) (int, error) {
	return unix.Dup(socketFD)
}

func (c *Conn) closeInner(err error) {
	fd := c.getFd()
	if c.isClosed() {
		return
	}
	c.cancel()

	c.getLogger().Debug("close conn", slog.Int64("fd", int64(fd)))
	c.multiEventLoop.del(c)
	c.parent.overflowRead.Delete(fd)
	c.parent.overflowWrite.Delete(fd)
	atomic.StoreInt64(&c.fd, -1)
	// c.closeOnce.Do(func() {
	// 	atomic.StorePointer((*unsafe.Pointer)((unsafe.Pointer)(&c.parent)), nil)
	// })
	atomic.StoreInt32(&c.closed, 1)
}

func (c *Conn) closeAndWaitOnMessage(wait bool, err error) {
	if c.isClosed() {
		return
	}
	if wait {
		c.waitOnMessageRun.Wait()
	}

	c.mu.Lock()
	if c.isClosed() {
		c.mu.Unlock()
		return
	}

	c.closeInner(err)
	c.mu.Unlock()
}

func (c *Conn) Close() {
	c.closeAndWaitOnMessage(false, nil)
}

func (c *Conn) getPtr() int {
	return int(uintptr(unsafe.Pointer(c)))
}

func (c *Conn) Write(b []byte) (n int, err error) {
	if c.isClosed() {
		return 0, ErrClosed
	}

	return c.writeOrWait(b)
}

func (c *Conn) writeOrWait(b []byte) (total int, err error) {
	// i 的目的是debug的时候使用
	var n int
	fd := c.getFd()
	for i := 0; len(b) > 0; i++ {

		// 直接写入数据
		n, err = unix.Write(int(fd), b)
		// 统计调用	unix.Write的次数
		c.multiEventLoop.addWriteSyscall()

		if err != nil {
			// 如果是EAGAIN或EINTR错误，说明是写缓冲区满了，或者被信号中断，将数据写入缓冲区
			if errors.Is(err, unix.EINTR) {
				continue
			}

			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {

				if err := c.multiEventLoop.addWrite(c); err != nil {
					return total, err
				}

				c.getLogger().Error("writeOrWait",
					"write_eagin", err.Error(),
					"fd", c.getFd(),
					"b.len", len(b),
					"nwrite", total)
				select {
				case <-c.canBeWrite:
				case <-c.ctx.Done():
					return total, c.ctx.Err()
				}

				if err := c.multiEventLoop.delWrite(c); err != nil {
					return total, err
				}
				// 被唤醒后，继续写入数据
				continue
			}

			c.getLogger().Error("writeOrWait", "err", err.Error(), slog.Int64("fd", c.fd), slog.Int("b.len", len(b)))
			c.closeInner(err)
			return total, err
		}

		if n > 0 {
			b = b[n:]
			total += n
		}
	}

	return total, nil
}

// 该函主要用于唤醒等待写事件的协程
func (c *Conn) wakeUpWrite() (err error) {
	if c.isClosed() {
		return ErrClosed
	}

	select {
	case <-c.ctx.Done():
		return
	case c.canBeWrite <- struct{}{}:
	default:
	}

	c.getLogger().Debug("writeOrWait_wakup", "fd", c.getFd())
	return err
}

// kqueu/epoll模式下，读取数据
// 该函数从缓冲区读取数据，并且解析出websocket frame
// 有几种情况需要处理下
// 1. 缓冲区空间不句够，需要扩容
// 2. 缓冲区数据不够，并且一次性读取了多个frame
func (c *Conn) processWebsocketFrame() (n int, err error) {
	// 1. 处理frame header
	if !c.useIoUring() {
		// 不使用io_uring的直接调用read获取buffer数据
		for i := 0; ; i++ {
			fd := atomic.LoadInt64(&c.fd)
			n, err = unix.Read(int(fd), (*c.rbuf)[c.rw:])
			c.multiEventLoop.addReadSyscall()
			// fmt.Printf("i = %d, n = %d, fd = %d, rbuf = %d, rw:%d, err = %v, %v, payload:%d\n", i, n, c.fd, len((*c.rbuf)[c.rw:]), c.rw+n, err, time.Now(), c.rh.PayloadLen)
			if err != nil {
				// 信号中断，继续读
				if errors.Is(err, unix.EINTR) {
					continue
				}
				// 出错返回
				if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
					return 0, err
				}
				// 缓冲区没有数据，等待可读
				err = nil
				break
			}

			// 读到eof，直接关闭
			if n == 0 && len((*c.rbuf)[c.rw:]) > 0 {
				go func() {
					c.closeAndWaitOnMessage(false, io.EOF)
					c.OnClose(c, io.EOF)
				}()
				return
			}

			if n > 0 {
				c.rw += n
			}

			if len((*c.rbuf)[c.rw:]) == 0 {
				// 说明缓存区已经满了。需要扩容
				// 并且如果使用epoll ET mode，需要继续读取，直到返回EAGAIN, 不然会丢失数据
				// 结合以上两种，缓存区满了就直接处理frame，解析出payload的长度，得到一个刚刚好的缓存区
				if _, err := c.readHeader(); err != nil {
					return 0, fmt.Errorf("read header err: %w", err)
				}
				if _, err := c.readPayloadAndCallback(); err != nil {
					return 0, fmt.Errorf("read header err: %w", err)
				}

				// TODO
				if len((*c.rbuf)[c.rw:]) == 0 {
					//
					// panic(fmt.Sprintf("需要扩容:rw(%d):rr(%d):currState(%v)", c.rw, c.rr, c.curState.String()))
				}
				continue
			}
		}
	}

	for i := 0; ; i++ {
		sucess, err := c.readHeader()
		if err != nil {
			return 0, fmt.Errorf("read header err: %w", err)
		}

		if !sucess {
			return 0, nil
		}
		sucess, err = c.readPayloadAndCallback()
		if err != nil {
			return 0, fmt.Errorf("read header err: %w", err)
		}

		if !sucess {
			return 0, nil
		}
	}
}

func closeFd(fd int) {
	unix.Close(int(fd))
}
