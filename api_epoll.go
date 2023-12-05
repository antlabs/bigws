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

//go:build linux
// +build linux

package greatws

import (
	"errors"
	"io"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (

	// EPOLLET .
	EPOLLET = 0x80000000
)

const (
	// 水平触发
	ltRead      = uint32(unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN)
	ltWrite     = uint32(unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN | unix.EPOLLOUT)
	ltDelWrite  = uint32(ltRead)
	ltResetRead = uint32(0)

	// 垂直触发
	etRead      = uint32(unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN | unix.EPOLLOUT | EPOLLET)
	etWrite     = uint32(0)
	etDelWrite  = uint32(0)
	etResetRead = uint32(0)

	// 一次性触发
	etReadOneShot      = uint32(unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN | unix.EPOLLOUT | EPOLLET | unix.EPOLLONESHOT)
	etWriteOneShot     = uint32(etReadOneShot)
	etDelWriteOneShot  = uint32(0)
	etResetReadOneShot = uint32(etReadOneShot)
)

func getReadWriteDeleteReset(oneShot bool, et bool, lt bool) (uint32, uint32, uint32, uint32) {
	if oneShot {
		return etReadOneShot, etWriteOneShot, etDelWriteOneShot, etResetReadOneShot
	}

	if et {
		return etRead, etWrite, etDelWrite, etResetRead
	}

	if lt {
		return ltRead, ltWrite, ltDelWrite, ltResetRead
	}

	return 0, 0, 0, 0
}

type epollState struct {
	epfd   int
	events []unix.EpollEvent

	parent  *EventLoop
	rev     uint32
	wev     uint32
	dwEv    uint32 // delete write event
	resetEv uint32
}

// 创建epoll handler
func apiEpollCreate(size int, parent *EventLoop) (la linuxApi, err error) {
	var e epollState
	e.epfd, err = unix.EpollCreate1(0)
	if err != nil {
		return nil, err
	}

	e.events = make([]unix.EpollEvent, size)
	e.parent = parent
	e.rev, e.wev, e.dwEv, e.resetEv = getReadWriteDeleteReset(false, false, true)
	return &e, nil
}

// 释放
func (e *epollState) apiFree() {
	unix.Close(e.epfd)
}

// 重装添加读事件
func (e *epollState) resetRead(c *Conn) error {
	fd := c.getFd()
	if e.resetEv > 0 {
		return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_MOD, int(fd), &unix.EpollEvent{
			Fd:     int32(fd),
			Events: e.resetEv,
		})
	}
	return nil
}

// 新加读事件
func (e *epollState) addRead(c *Conn) error {
	if e.rev > 0 {
		fd := int(c.getFd())
		return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{
			Fd:     int32(fd),
			Events: e.rev,
		})
	}
	return nil
}

// 新加写事件
func (e *epollState) addWrite(c *Conn) error {
	if e.wev > 0 {

		fd := int(c.getFd())
		return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_MOD, fd, &unix.EpollEvent{
			Fd:     int32(fd),
			Events: e.wev,
		})
	}

	return nil
}

// 删除写事件
func (e *epollState) delWrite(c *Conn) error {
	if e.dwEv > 0 {

		fd := int(c.getFd())
		return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_MOD, fd, &unix.EpollEvent{
			Fd:     int32(fd),
			Events: e.dwEv,
		})
	}
	return nil
}

// 删除事件
func (e *epollState) del(fd int) error {
	return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_DEL, fd, &unix.EpollEvent{Fd: int32(fd)})
}

func toMsec(tv time.Duration) int {
	if tv < 0 {
		return -1
	}
	return int(tv / time.Millisecond)
}

// 事件循环
func (e *epollState) apiPoll(tv time.Duration) (numEvents int, err error) {
	numEvents, err = unix.EpollWait(e.epfd, e.events, toMsec(tv))
	// 统计poll次数
	e.parent.parent.addPollEv()
	if err != nil {
		if errors.Is(err, unix.EINTR) {
			return 0, nil
		}
		return 0, err
	}

	// 处理overflow的连接
	overflow := e.parent.overflow
	if !e.parent.parent.t.highLoad() && len(overflow) > 0 {
		e.parent.parent.Debug("process overflow")
		for fd, conn := range overflow {
			_, err = conn.processWebsocketFrame()
			if err != nil {
				go conn.closeAndWaitOnMessage(true, err)
				continue
			}

			if err := e.resetRead(conn); err != nil {
				go conn.closeAndWaitOnMessage(true, err)
				continue
			}
			delete(overflow, fd)
		}
	}

	if numEvents > 0 {
		for i := 0; i < numEvents; i++ {
			ev := &e.events[i]
			conn := e.parent.parent.getConn(int(ev.Fd))
			if conn == nil {
				unix.Close(int(ev.Fd))
				continue
			}

			// 如果是关闭事件，直接关闭
			if ev.Events&unix.EPOLLRDHUP > 0 {
				go conn.closeAndWaitOnMessage(true, io.EOF)
				continue
			}
			// 如果是错误事件，直接关闭
			if ev.Events&(unix.EPOLLERR|unix.EPOLLHUP) > 0 {
				go conn.closeAndWaitOnMessage(false, io.EOF)
				continue
			}

			// 如果是写事件，唤醒下write 阻塞的协程
			if ev.Events&unix.EPOLLOUT > 0 {
				e.parent.parent.addWriteEv()
				// 唤醒下write 阻塞的协程
				conn.wakeUpWrite()
			}

			// 先判断下是否是高负载, 如果是高负载，直接放入到overflow中, 过段时间再处理
			if e.parent.parent.t.highLoad() {
				if atomic.LoadInt32(&conn.nwrite) > 0 {
					e.parent.parent.addWriteEv()
				}
				e.parent.overflow[conn.getFd()] = conn
				continue
			}

			if ev.Events&unix.EPOLLIN > 0 {
				e.parent.parent.addReadEv()

				// 读取数据，这里要发行下websocket的解析变成流式解析
				_, err = conn.processWebsocketFrame()
				if err != nil {
					go conn.closeAndWaitOnMessage(true, err)
					continue
				}

				if err := e.resetRead(conn); err != nil {
					go conn.closeAndWaitOnMessage(true, err)
					continue
				}
			}

		}
	}

	return numEvents, nil
}

func (e *epollState) apiName() string {
	return "epoll"
}
