// Copyright 2023-2023 antlabs. All rights reserved.
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
	"fmt"
	"time"
)

// linux 有两个api，一个是epoll，一个是io_uring
type apiState struct {
	linuxApi
}

type linuxApi interface {
	apiFree()
	apiPoll(tv time.Duration) (retVal int, err error)
	apiName() string
	addRead(c *Conn) error
	addWrite(c *Conn) error
	delWrite(c *Conn) error
}

// 创建
func (e *EventLoop) apiCreate(size int, flag evFlag) (err error) {
	var state apiState

	if flag&EVENT_EPOLL != 0 {
		la, err := apiEpollCreate(size, e)
		if err != nil {
			return err
		}
		state.linuxApi = la
	} else if flag&EVENT_IOURING != 0 {
		la, err := apiIoUringCreate(e, 16384)
		if err != nil {
			return err
		}
		state.linuxApi = la
	} else {
		return fmt.Errorf("not support api")
	}

	e.apiState = &state
	return nil
}
