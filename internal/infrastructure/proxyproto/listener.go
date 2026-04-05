// Package proxyproto implements PROXY protocol v1 detection for net.Listener.
//
// Connections that begin with a PROXY protocol v1 header get the real client
// address extracted and exposed via RemoteAddr(). Non-PROXY connections pass
// through unchanged. This allows the same listener to serve both direct clients
// (browser) and gateway-proxied traffic (workspace containers via haproxy).
package proxyproto

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"time"
)

// Listener wraps a net.Listener and transparently parses PROXY protocol v1
// headers from incoming connections.
type Listener struct {
	net.Listener
}

// NewListener wraps an existing listener with PROXY protocol v1 support.
func NewListener(inner net.Listener) *Listener {
	return &Listener{Listener: inner}
}

func (l *Listener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return parseProxyConn(conn)
}

// parseProxyConn peeks at the first bytes of a connection. If they match the
// PROXY protocol v1 signature, the header is consumed and the real client
// address is exposed via RemoteAddr(). Otherwise, the bytes remain in the
// buffer and the original address is preserved.
func parseProxyConn(c net.Conn) (net.Conn, error) {
	reader := bufio.NewReaderSize(c, 512)

	// Brief deadline for protocol detection — both browsers and haproxy send
	// data immediately, so 5 s is generous.
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	peek, err := reader.Peek(6)
	c.SetReadDeadline(time.Time{})

	if err != nil || string(peek) != "PROXY " {
		return &bufferedConn{Conn: c, reader: reader}, nil
	}

	// Read the full PROXY line: "PROXY TCP4 <src> <dst> <srcport> <dstport>\r\n"
	line, err := reader.ReadString('\n')
	if err != nil {
		return &bufferedConn{Conn: c, reader: reader}, nil
	}
	line = strings.TrimRight(line, "\r\n")

	parts := strings.Fields(line)
	if len(parts) < 6 {
		return &bufferedConn{Conn: c, reader: reader}, nil
	}

	srcIP := net.ParseIP(parts[2])
	srcPort, _ := strconv.Atoi(parts[4])

	if srcIP == nil {
		return &bufferedConn{Conn: c, reader: reader}, nil
	}

	return &bufferedConn{
		Conn:   c,
		reader: reader,
		addr:   &net.TCPAddr{IP: srcIP, Port: srcPort},
	}, nil
}

// bufferedConn wraps a net.Conn with a bufio.Reader (to return peeked bytes)
// and an optional overridden remote address (from PROXY protocol).
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
	addr   net.Addr // nil = use original RemoteAddr
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (c *bufferedConn) RemoteAddr() net.Addr {
	if c.addr != nil {
		return c.addr
	}
	return c.Conn.RemoteAddr()
}
