package upstream

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func NewTransport(proxyAddress string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 512
	transport.MaxIdleConnsPerHost = 128
	transport.IdleConnTimeout = 90 * time.Second
	if strings.TrimSpace(proxyAddress) == "" {
		transport.Proxy = nil
		return transport, nil
	}
	parsed, err := url.Parse(proxyAddress)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(parsed)
	case "socks5", "socks5h":
		dialer, err := newSOCKS5Dialer(parsed)
		if err != nil {
			return nil, err
		}
		transport.Proxy = nil
		transport.DialContext = dialer.DialContext
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
	return transport, nil
}

type socks5Dialer struct {
	address  string
	username string
	password string
	dialer   net.Dialer
}

func newSOCKS5Dialer(proxyURL *url.URL) (*socks5Dialer, error) {
	if proxyURL.Hostname() == "" {
		return nil, errors.New("SOCKS5 proxy host is empty")
	}
	port := proxyURL.Port()
	if port == "" {
		port = "1080"
	}
	result := &socks5Dialer{address: net.JoinHostPort(proxyURL.Hostname(), port), dialer: net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}}
	if proxyURL.User != nil {
		result.username = proxyURL.User.Username()
		result.password, _ = proxyURL.User.Password()
	}
	if len(result.username) > 255 || len(result.password) > 255 {
		return nil, errors.New("SOCKS5 credentials exceed 255 bytes")
	}
	return result, nil
}

func (d *socks5Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	connection, err := d.dialer.DialContext(ctx, "tcp", d.address)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := d.handshake(connection, address); err != nil {
		_ = connection.Close()
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, nil
}

func (d *socks5Dialer) handshake(connection net.Conn, address string) error {
	methods := []byte{0x00}
	if d.username != "" {
		methods = append(methods, 0x02)
	}
	if _, err := connection.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] == 0xff {
		return errors.New("SOCKS5 proxy rejected authentication methods")
	}
	if response[1] == 0x02 {
		packet := []byte{0x01, byte(len(d.username))}
		packet = append(packet, d.username...)
		packet = append(packet, byte(len(d.password)))
		packet = append(packet, d.password...)
		if _, err := connection.Write(packet); err != nil {
			return err
		}
		if _, err := io.ReadFull(connection, response); err != nil {
			return err
		}
		if response[1] != 0x00 {
			return errors.New("SOCKS5 username/password authentication failed")
		}
	}

	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid destination port")
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("destination host exceeds 255 bytes")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err := connection.Write(request); err != nil {
		return err
	}
	reader := bufio.NewReader(connection)
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if header[0] != 0x05 || header[1] != 0x00 {
		return fmt.Errorf("SOCKS5 connect failed with code %d", header[1])
	}
	var skip int
	switch header[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		length, err := reader.ReadByte()
		if err != nil {
			return err
		}
		skip = int(length)
	default:
		return errors.New("SOCKS5 proxy returned an unknown address type")
	}
	_, err = io.CopyN(io.Discard, reader, int64(skip+2))
	return err
}
