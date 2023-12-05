//go:build linux
// +build linux

package greatws

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/pawelgaczynski/giouring"
)

type iouringState struct {
	mu          sync.Mutex
	ring        *giouring.Ring // ring 对象
	ringEntries uint32
	parent      *EventLoop
	submitter
}

func apiIoUringCreate(el *EventLoop, ringEntries uint32) (la linuxApi, err error) {
	var iouringState iouringState

	ring, err := giouring.CreateRing(ringEntries)
	iouringState.submitter = newBatchSubmitter(ring)
	iouringState.ring = ring
	iouringState.parent = el
	return &iouringState, nil
}

func (e *iouringState) apiFree() {
}

type iouringConn struct {
	*iouringState
	fd int
}

// iouring 模式下，读取数据
func (c *Conn) processWebsocketFrameOnlyIoUring() (n int, err error) {
	// 尽可能消耗完rbuf里面的数据
	for {
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

func (e *iouringState) addRead(c *Conn) error {
	e.mu.Lock()
	entry := e.ring.GetSQE()
	e.mu.Unlock()
	if entry == nil {
		return errors.New("addRead: fail:GetSQE is nil")
	}

	entry.PrepareRecv(
		int(c.fd),
		uintptr((*reflect.SliceHeader)(unsafe.Pointer(c.rbuf)).Data+uintptr(c.rw)),
		uint32(len((*c.rbuf)[c.rw:])),
		0)
	entry.UserData = encodeUserData(uint32(c.fd), opRead, 0)
	return nil
}

// write使用epoll托管
func (e *iouringState) addWrite(c *Conn) error {
	return nil
}

func (e *iouringState) del(c *Conn) error {
	fd := c.fd

	entry := e.ring.GetSQE()
	if entry == nil {
		return errors.New("del: fail: GetSQE is nil")
	}

	entry.PrepareClose(int(fd))
	entry.UserData = encodeUserData(uint32(fd), opClose, 0)

	return nil
}

func (e *iouringState) getLogger() *slog.Logger {
	return e.parent.parent.Logger
}

func (e *iouringState) getConn(fd uint32) *Conn {
	return e.parent.parent.getConn(int(fd))
}

// io-uring 处理事件的入口函数
func (e *iouringState) processConn(cqe *giouring.CompletionQueueEvent) error {
	// c := (*Conn)(unsafe.Pointer(uintptr(cqe.UserData)))
	fd, op, writeSeq := decodeUserData(cqe.UserData)

	c := e.getConn(fd)
	if c == nil {
		e.getLogger().Warn("processConn, conn is nil", "fd", fd, "userdata", cqe.UserData)
		return nil
	}
	if op&opRead > 0 {
		if err := c.processRead(cqe); err != nil {
			return err
		}
	}
	if op&opWrite > 0 {
		if err := c.processWrite(cqe, writeSeq); err != nil {
			return err
		}
	}
	if op&opClose > 0 {
		if err := c.processClose(cqe); err != nil {
			return err
		}
	}
	return nil
}

func (e *iouringState) run(timeout time.Duration) error {
	var err error
	cqes := make([]*giouring.CompletionQueueEvent, 256 /*TODO:*/)

	e.mu.Lock()
	if err = e.submit(); err != nil {
		e.mu.Unlock()
		if errors.Is(err, ErrSkippable) {
			return nil
		}

		return err
	}
	e.mu.Unlock()
	numberOfCQEs := e.ring.PeekBatchCQE(cqes)

	var i uint32
	for i = 0; i < numberOfCQEs; i++ {
		cqe := cqes[i]

		err = e.processConn(cqe)
		if err != nil {
			e.advance(i + 1)
			return err
		}
	}
	e.advance(numberOfCQEs)

	return nil
}

func (e *iouringState) apiPoll(tv time.Duration) (retVal int, err error) {
	if err := e.run(time.Millisecond * 333); err != nil {
		return 0, err
	}
	return 0, nil
}

func (e *iouringState) delWrite(c *Conn) error {
	return nil
}

func (e *iouringState) apiName() string {
	return "io_uring"
}
