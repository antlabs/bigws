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
	"time"

	"golang.org/x/sys/unix"
)

const (

	// EPOLLET .
	EPOLLET = 0x80000000
)

type epollState struct {
	epfd   int
	events []unix.EpollEvent

	et     bool
	parent *EventLoop
}

// 创建epoll handler
func apiEpollCreate(parent *EventLoop) (la linuxApi, err error) {
	var e epollState
	e.epfd, err = unix.EpollCreate1(0)
	if err != nil {
		return nil, err
	}

	e.events = make([]unix.EpollEvent, 128)
	e.parent = parent
	e.et = true
	return &e, nil
}

// 释放
func (e *epollState) apiFree() {
	unix.Close(e.epfd)
}

// 新加读事件
func (e *epollState) addRead(c *Conn) error {
	fd := int(c.getFd())
	return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{
		Fd: int32(fd),
		// 来自man 手册
		// When  used as an edge-triggered interface, for performance reasons,
		// it is possible to add the file descriptor inside the epoll interface (EPOLL_CTL_ADD) once by specifying (EPOLLIN|EPOLLOUT).
		// This allows you to avoid con‐
		// tinuously switching between EPOLLIN and EPOLLOUT calling epoll_ctl(2) with EPOLL_CTL_MOD.
		Events: unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN | unix.EPOLLOUT | EPOLLET,
	})
}

// 新加写事件
func (e *epollState) addWrite(c *Conn, writeSeq uint16) error {
	if e.et {
		return nil
	}

	fd := int(c.getFd())
	return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_MOD, fd, &unix.EpollEvent{
		Fd:     int32(fd),
		Events: unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN | unix.EPOLLOUT,
	})
}

// 删除写事件
func (e *epollState) delWrite(c *Conn) error {
	if e.et {
		return nil
	}

	fd := int(c.getFd())
	return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_MOD, fd, &unix.EpollEvent{
		Fd:     int32(fd),
		Events: unix.EPOLLERR | unix.EPOLLHUP | unix.EPOLLRDHUP | unix.EPOLLPRI | unix.EPOLLIN | EPOLLET,
	})
}

// 删除事件
func (e *epollState) del(fd int) error {
	return unix.EpollCtl(e.epfd, unix.EPOLL_CTL_DEL, fd, &unix.EpollEvent{Fd: int32(fd)})
}

// 事件循环
func (e *epollState) apiPoll(tv time.Duration) (retVal int, err error) {
	msec := -1
	if tv > 0 {
		msec = int(tv) / int(time.Millisecond)
	}

	retVal, err = unix.EpollWait(e.epfd, e.events, msec)
	e.parent.parent.addPollEvNum()
	if err != nil {
		if errors.Is(err, unix.EINTR) {
			return 0, nil
		}
		return 0, err
	}
	numEvents := 0
	if retVal > 0 {
		numEvents = retVal
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

			if ev.Events&(unix.EPOLLIN|unix.EPOLLRDHUP|unix.EPOLLHUP|unix.EPOLLERR) > 0 {
				e.parent.parent.addReadEvNum()

				// 读取数据，这里websocket的解析变成流式解析
				_, err = conn.processWebsocketFrame()
				if err != nil {
					go conn.closeAndWaitOnMessage(true, err)
					continue
				}
			}
			if ev.Events&unix.EPOLLOUT > 0 {
				e.parent.parent.addWriteEvNum()
				// 刷新下直接写入失败的数据
				conn.flushOrClose()
			}
			if ev.Events&(unix.EPOLLERR|unix.EPOLLHUP|unix.EPOLLRDHUP) > 0 {
				go conn.closeAndWaitOnMessage(false, io.EOF)
			}
		}

	}

	return numEvents, nil
}

func (e *epollState) apiName() string {
	return "epoll"
}
