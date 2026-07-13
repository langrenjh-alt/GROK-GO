package media

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

var ErrUnsafeURL = errors.New("unsafe outbound media URL")

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type SSRFPolicy struct {
	AllowedHosts []string
	AllowedPorts []string
	AllowHTTP    bool
	Resolver     Resolver
	DialTimeout  time.Duration
}

func ValidateURL(ctx context.Context, rawURL string, policy SSRFPolicy) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsafeURL, err)
	}
	if parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && !(policy.AllowHTTP && parsed.Scheme == "http")) {
		return nil, ErrUnsafeURL
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, ErrUnsafeURL
	}
	if len(policy.AllowedHosts) > 0 && !hostAllowed(host, policy.AllowedHosts) {
		return nil, ErrUnsafeURL
	}
	port := parsed.Port()
	if port == "" {
		port = map[bool]string{true: "443", false: "80"}[parsed.Scheme == "https"]
	}
	if len(policy.AllowedPorts) > 0 && !slices.Contains(policy.AllowedPorts, port) {
		return nil, ErrUnsafeURL
	}
	addresses, err := resolve(ctx, host, policy.Resolver)
	if err != nil || len(addresses) == 0 {
		if err == nil {
			err = errors.New("host did not resolve")
		}
		return nil, fmt.Errorf("%w: %v", ErrUnsafeURL, err)
	}
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return nil, ErrUnsafeURL
		}
	}
	return parsed, nil
}

func NewSafeTransport(base *http.Transport, policy SSRFPolicy) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	dialer := net.Dialer{Timeout: policy.DialTimeout, KeepAlive: 30 * time.Second}
	if dialer.Timeout <= 0 {
		dialer.Timeout = 15 * time.Second
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolve(ctx, host, policy.Resolver)
		if err != nil {
			return nil, err
		}
		for _, candidate := range addresses {
			if !publicIP(candidate.IP) {
				return nil, ErrUnsafeURL
			}
		}
		if len(addresses) == 0 {
			return nil, ErrUnsafeURL
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
	return transport
}

func NewSafeClient(base *http.Transport, policy SSRFPolicy) *http.Client {
	return &http.Client{
		Transport: NewSafeTransport(base, policy),
		Timeout:   60 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many media redirects")
			}
			_, err := ValidateURL(request.Context(), request.URL.String(), policy)
			return err
		},
	}
}

func resolve(ctx context.Context, host string, resolver Resolver) ([]net.IPAddr, error) {
	if parsed := net.ParseIP(host); parsed != nil {
		return []net.IPAddr{{IP: parsed}}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return resolver.LookupIPAddr(ctx, host)
}

func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	// RFC 6598 shared address space is not reported by IsPrivate.
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return !cgnat.Contains(address)
}

func hostAllowed(host string, allowed []string) bool {
	for _, candidate := range allowed {
		candidate = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".")
		if candidate == host || (strings.HasPrefix(candidate, "*.") && strings.HasSuffix(host, candidate[1:]) && host != candidate[2:]) {
			return true
		}
	}
	return false
}
