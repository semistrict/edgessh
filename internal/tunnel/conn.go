package tunnel

import (
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// WSConn wraps a WebSocket connection to implement net.Conn.
// This lets us pass it directly to ssh.NewClientConn.
type WSConn struct {
	ws     *websocket.Conn
	reader io.Reader
}

// Wrap exposes an existing WebSocket as a net.Conn.
func Wrap(ws *websocket.Conn) net.Conn {
	return &WSConn{ws: ws}
}

// Dial opens a WebSocket to the SSH tunnel endpoint and returns it as a net.Conn.
func Dial(wsURL, token string) (net.Conn, error) {
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	headers.Set("User-Agent", "edgessh")
	return DialWithHeaders(wsURL, headers)
}

// DialWithHeaders opens a WebSocket and returns it as a net.Conn.
func DialWithHeaders(wsURL string, headers http.Header) (net.Conn, error) {
	dialer := websocket.Dialer{}
	ws, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return nil, err
	}

	return &WSConn{ws: ws}, nil
}

func (c *WSConn) Read(p []byte) (int, error) {
	for {
		if c.reader != nil {
			n, err := c.reader.Read(p)
			if n > 0 {
				return n, nil
			}
			if err != io.EOF {
				return 0, err
			}
			c.reader = nil
		}

		_, reader, err := c.ws.NextReader()
		if err != nil {
			return 0, err
		}
		c.reader = reader
	}
}

func (c *WSConn) Write(p []byte) (int, error) {
	err := c.ws.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *WSConn) Close() error {
	return c.ws.Close()
}

func (c *WSConn) LocalAddr() net.Addr {
	return c.ws.LocalAddr()
}

func (c *WSConn) RemoteAddr() net.Addr {
	return c.ws.RemoteAddr()
}

func (c *WSConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *WSConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

func (c *WSConn) SetWriteDeadline(t time.Time) error {
	return c.ws.SetWriteDeadline(t)
}
