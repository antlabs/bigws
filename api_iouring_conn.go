//go:build linux
// +build linux

package bigws

import (
	"unsafe"

	"github.com/pawelgaczynski/giouring"
)

func processConn(cqe *giouring.CompletionQueueEvent) error {
	c := (*Conn)(unsafe.Pointer(uintptr(cqe.UserData)))
	operation := c.operation
	if operation&opRead > 0 {
		if err := c.processRead(cqe); err != nil {
		}
	}
	if operation&opWrite > 0 {
		if err := c.processWrite(cqe); err != nil {
		}
	}
	if operation&opClose > 0 {
		if err := c.processClose(cqe); err != nil {
		}
	}
	return nil
}

func (c *Conn) outboundReadAddress() unsafe.Pointer {
	return c.outboundBuffer.ReadAddress()
}

func (c *Conn) inboundWriteAddress() unsafe.Pointer {
	return c.inboundBuffer.WriteAddress()
}

func (c *Conn) processRead(cqe *giouring.CompletionQueueEvent) error {
	if cqe.Res <= 0 {
		go c.closeAndWaitOnMessage(true)
		return nil
	}
	return nil
}

func (c *Conn) processWrite(cqe *giouring.CompletionQueueEvent) error {
	c.getLogger().Debug("write res", "res", cqe.Res)
	return nil
}

func (c *Conn) processClose(cqe *giouring.CompletionQueueEvent) error {
	return nil
}
