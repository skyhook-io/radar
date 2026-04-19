package cloud

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn adapts a gorilla WebSocket into a net.Conn so yamux can run on top
// of it.
//
// Writes are serialized (gorilla forbids concurrent WriteMessage). Reads
// chain successive binary frames into a single byte stream so yamux sees a
// continuous transport.
type wsConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	reader  io.Reader // current-frame reader; nil means "fetch next frame"
}

func newWSConn(ws *websocket.Conn) *wsConn { return &wsConn{ws: ws} }

func (c *wsConn) Read(b []byte) (int, error) {
	for {
		if c.reader == nil {
			_, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			c.reader = r
		}
		n, err := c.reader.Read(b)
		if n > 0 {
			if err == io.EOF {
				c.reader = nil
				err = nil
			}
			return n, err
		}
		if err == io.EOF {
			c.reader = nil
			continue
		}
		return 0, err
	}
}

func (c *wsConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *wsConn) Close() error          { return c.ws.Close() }
func (c *wsConn) LocalAddr() net.Addr   { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr  { return c.ws.RemoteAddr() }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

var _ net.Conn = (*wsConn)(nil)
