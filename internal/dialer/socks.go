package dialer

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
)

func dialSOCKS5(ctx context.Context, e Endpoint, target string) (net.Conn, error) {
	conn, err := dialRaw(ctx, e)
	if err != nil {
		return nil, err
	}
	if err := socks5(conn, e, target); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func socks5(conn net.Conn, e Endpoint, target string) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("socks target: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("socks port")
	}
	method := byte(0x00)
	if e.Username != "" || e.Password != "" {
		method = 0x02
	}
	if _, err := conn.Write([]byte{0x05, 0x01, method}); err != nil {
		return fmt.Errorf("socks greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks greeting: %w", err)
	}
	if reply[0] != 0x05 || reply[1] != method {
		return fmt.Errorf("socks method rejected")
	}
	if method == 0x02 {
		if err := socksAuth(conn, e); err != nil {
			return err
		}
	}
	req, err := socksRequest(host, port)
	if err != nil {
		return err
	}
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks connect: %w", err)
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return fmt.Errorf("socks connect: %w", err)
	}
	if hdr[0] != 0x05 {
		return fmt.Errorf("socks bad version")
	}
	if hdr[1] != 0x00 {
		return fmt.Errorf("socks connect status %d", hdr[1])
	}
	return discardSocksAddr(conn, hdr[3])
}

func socksAuth(conn net.Conn, e Endpoint) error {
	user := []byte(e.Username)
	pass := []byte(e.Password)
	if len(user) > 255 || len(pass) > 255 {
		return fmt.Errorf("socks credentials are too long")
	}
	msg := make([]byte, 0, 3+len(user)+len(pass))
	msg = append(msg, 0x01, byte(len(user)))
	msg = append(msg, user...)
	msg = append(msg, byte(len(pass)))
	msg = append(msg, pass...)
	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("socks auth: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks auth: %w", err)
	}
	if reply[1] != 0x00 {
		return fmt.Errorf("socks authentication failed")
	}
	return nil
}

func socksRequest(host string, port int) ([]byte, error) {
	buf := []byte{0x05, 0x01, 0x00}
	ip := net.ParseIP(host)
	switch {
	case ip.To4() != nil:
		buf = append(buf, 0x01)
		buf = append(buf, ip.To4()...)
	case ip != nil:
		buf = append(buf, 0x04)
		buf = append(buf, ip.To16()...)
	default:
		if len(host) == 0 || len(host) > 255 {
			return nil, fmt.Errorf("socks host length")
		}
		buf = append(buf, 0x03, byte(len(host)))
		buf = append(buf, host...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	buf = append(buf, p[:]...)
	return buf, nil
}

func discardSocksAddr(r io.Reader, atyp byte) error {
	var n int
	switch atyp {
	case 0x01:
		n = 4
	case 0x04:
		n = 16
	case 0x03:
		lenb := make([]byte, 1)
		if _, err := io.ReadFull(r, lenb); err != nil {
			return fmt.Errorf("socks bind addr: %w", err)
		}
		n = int(lenb[0])
	default:
		return fmt.Errorf("socks bind address type %d", atyp)
	}
	if _, err := io.CopyN(io.Discard, r, int64(n+2)); err != nil {
		return fmt.Errorf("socks bind addr: %w", err)
	}
	return nil
}
