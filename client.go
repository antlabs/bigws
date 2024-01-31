// Copyright 2023-2024 antlabs. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package greatws

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/antlabs/wsutil/hostname"
)

var (
	defaultTimeout = time.Minute * 30
	strExtensions  = "permessage-deflate; server_no_context_takeover; client_no_context_takeover"
)

type DialOption struct {
	Header               http.Header
	u                    *url.URL
	tlsConfig            *tls.Config
	dialTimeout          time.Duration
	bindClientHttpHeader *http.Header // 握手成功之后, 客户端获取http.Header,
	Config
}

func ClientOptionToConf(opts ...ClientOption) *DialOption {
	var dial DialOption
	dial.defaultSetting()
	for _, o := range opts {
		o(&dial)
	}
	return &dial
}

func DialConf(rawUrl string, conf *DialOption) (*Conn, error) {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return nil, err
	}

	conf.u = u
	conf.dialTimeout = defaultTimeout
	if conf.Header == nil {
		conf.Header = make(http.Header)
	}

	return conf.Dial()
}

// https://datatracker.ietf.org/doc/html/rfc6455#section-4.1
// 又是一顿if else, 咬文嚼字
func Dial(rawUrl string, opts ...ClientOption) (*Conn, error) {
	var dial DialOption
	u, err := url.Parse(rawUrl)
	if err != nil {
		return nil, err
	}

	dial.u = u
	dial.dialTimeout = defaultTimeout
	if dial.Header == nil {
		dial.Header = make(http.Header)
	}

	dial.defaultSetting()
	for _, o := range opts {
		o(&dial)
	}

	return dial.Dial()
}

// 准备握手的数据
func (d *DialOption) handshake() (*http.Request, string, error) {
	switch {
	case d.u.Scheme == "wss":
		d.u.Scheme = "https"
	case d.u.Scheme == "ws":
		d.u.Scheme = "http"
	default:
		return nil, "", fmt.Errorf("Unknown scheme, only supports ws:// or wss://: got %s", d.u.Scheme)
	}

	// 满足4.1
	// 第2点 GET约束http 1.1版本约束
	req, err := http.NewRequest("GET", d.u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	// 第5点
	d.Header.Add("Upgrade", "websocket")
	// 第6点
	d.Header.Add("Connection", "Upgrade")
	// 第7点
	secWebSocket := secWebSocketAccept()
	d.Header.Add("Sec-WebSocket-Key", secWebSocket)
	// TODO 第8点
	// 第9点
	d.Header.Add("Sec-WebSocket-Version", "13")

	if d.decompression && d.compression {
		d.Header.Add("Sec-WebSocket-Extensions", strExtensions)
	}

	req.Header = d.Header
	return req, secWebSocket, nil
}

// 检查服务端响应的数据
// 4.2.2.5
func (d *DialOption) validateRsp(rsp *http.Response, secWebSocket string) error {
	if rsp.StatusCode != 101 {
		return fmt.Errorf("%w %d", ErrWrongStatusCode, rsp.StatusCode)
	}

	// 第2点
	if !strings.EqualFold(rsp.Header.Get("Upgrade"), "websocket") {
		return ErrUpgradeFieldValue
	}

	// 第3点
	if !strings.EqualFold(rsp.Header.Get("Connection"), "Upgrade") {
		return ErrConnectionFieldValue
	}

	// 第4点
	if !strings.EqualFold(rsp.Header.Get("Sec-WebSocket-Accept"), secWebSocketAcceptVal(secWebSocket)) {
		return ErrSecWebSocketAccept
	}

	// TODO 5点

	// TODO 6点
	return nil
}

// wss已经修改为https
func (d *DialOption) tlsConn(c net.Conn) net.Conn {
	if d.u.Scheme == "https" {
		cfg := d.tlsConfig
		if cfg == nil {
			cfg = &tls.Config{}
		} else {
			cfg = cfg.Clone()
		}

		if cfg.ServerName == "" {
			host := d.u.Host
			if pos := strings.Index(host, ":"); pos != -1 {
				host = host[:pos]
			}
			cfg.ServerName = host
		}
		return tls.Client(c, cfg)
	}

	return c
}

func (d *DialOption) Dial() (c *Conn, err error) {
	if d.Config.multiEventLoop == nil {
		return nil, ErrEventLoopEmpty
	}

	if !d.Config.multiEventLoop.isStart() {
		return nil, ErrEventLoopNotStart
	}
	req, secWebSocket, err := d.handshake()
	if err != nil {
		return nil, err
	}

	begin := time.Now()
	hostName := hostname.GetHostName(d.u)
	conn, err := net.Dial("tcp", hostName)
	if err != nil {
		return nil, err
	}

	dialDuration := time.Since(begin)

	conn = d.tlsConn(conn)
	defer func() {
		if err != nil && conn != nil {
			conn.Close()
			conn = nil
		}
	}()

	if to := d.dialTimeout - dialDuration; to > 0 {
		if err = conn.SetDeadline(time.Now().Add(to)); err != nil {
			return
		}
	}
	err = conn.SetDeadline(time.Time{})
	if err != nil {
		return nil, err
	}

	if err = req.Write(conn); err != nil {
		return
	}

	br := bufio.NewReader(bufio.NewReader(conn))
	rsp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, err
	}

	if d.bindClientHttpHeader != nil {
		*d.bindClientHttpHeader = rsp.Header.Clone()
	}

	cd := maybeCompressionDecompression(rsp.Header)
	if d.decompression {
		d.decompression = cd
	}
	if d.compression {
		d.compression = cd
	}

	if err = d.validateRsp(rsp, secWebSocket); err != nil {
		return
	}

	fd, err := getFdFromConn(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// 已经dup了一份fd，所以这里可以关闭
	if err = conn.Close(); err != nil {
		return nil, err
	}
	c = newConn(int64(fd), true, &d.Config)
	d.Callback.OnOpen(c)
	if err = d.Config.multiEventLoop.add(c); err != nil {
		return nil, err
	}
	return c, nil
}
