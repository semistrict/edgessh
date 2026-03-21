package tunnel

import (
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Proxy creates a local TCP proxy that bridges SSH traffic over a WebSocket.
// This matches wrangler's createSshTcpProxy implementation exactly.
type Proxy struct {
	wsURL   string
	wsToken string
	listener net.Listener
}

// NewProxy creates a new TCP-to-WebSocket proxy.
func NewProxy(wsURL, wsToken string) *Proxy {
	return &Proxy{
		wsURL:   wsURL,
		wsToken: wsToken,
	}
}

// Start starts the proxy and returns the local port.
func (p *Proxy) Start() (int, error) {
	var err error
	p.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("starting proxy listener: %w", err)
	}

	go p.accept()

	return p.listener.Addr().(*net.TCPAddr).Port, nil
}

// Close stops the proxy.
func (p *Proxy) Close() {
	if p.listener != nil {
		p.listener.Close()
	}
}

func (p *Proxy) accept() {
	hasConnection := false

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}

		// Only allow one connection at a time (same as wrangler)
		if hasConnection {
			conn.Close()
			continue
		}
		hasConnection = true

		go p.handleConnection(conn)
	}
}

func (p *Proxy) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Connect WebSocket with auth header
	dialer := websocket.Dialer{}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+p.wsToken)
	headers.Set("User-Agent", "edgessh")

	ws, _, err := dialer.Dial(p.wsURL, headers)
	if err != nil {
		fmt.Printf("WebSocket connection error: %v\n", err)
		return
	}
	defer ws.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// TCP → WebSocket
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				ws.Close()
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// WebSocket → TCP
	go func() {
		defer wg.Done()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				conn.Close()
				return
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()

	wg.Wait()
}
