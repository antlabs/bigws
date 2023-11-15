//go:build linux
// +build linux

package bigws

import (
	"io"

	"github.com/pawelgaczynski/giouring"
)

func (c *Conn) processRead(cqe *giouring.CompletionQueueEvent) error {
	// 返回值小于等于0，表示读取完毕，关闭连接
	if cqe.Res <= 0 {
		go c.closeAndWaitOnMessage(true, io.EOF)
		return nil
	}

	c.getLogger().Debug("read res", "res", cqe.Res, "fd", c.fd)

	// 处理websocket数据
	c.rw += int(cqe.Res)
	_, err := c.processWebsocketFrameOnlyIoUring()
	if err != nil {
		c.getLogger().Error("processWebsocketFrameOnlyIoUring", "err", err)
		return err
	}

	parent := c.getParent()
	if parent == nil {
		c.processClose(cqe)
		c.getLogger().Debug("parent is nil", "close", c.closed)
		return nil
	}

	if err := parent.addRead(c); err != nil {
		return err
	}

	return nil
}

func (c *Conn) processWrite(cqe *giouring.CompletionQueueEvent) error {
	c.getLogger().Debug("write res", "res", cqe.Res)
	return nil
}

func (c *Conn) processClose(cqe *giouring.CompletionQueueEvent) error {
	go c.closeAndWaitOnMessage(true, io.EOF)
	return nil
}
