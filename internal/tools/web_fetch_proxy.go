package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type browserProxy struct {
	listener net.Listener
	server   *http.Server
	resolver netIPResolver
	dialer   *net.Dialer
	done     chan struct{}
	once     sync.Once
}

func newBrowserProxy(resolver netIPResolver) (*browserProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &browserProxy{
		listener: listener,
		resolver: resolver,
		dialer:   &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
		done:     make(chan struct{}),
	}
	proxy.server = &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(proxy.done)
		_ = proxy.server.Serve(listener)
	}()
	return proxy, nil
}

func (p *browserProxy) URL() string { return "http://" + p.listener.Addr().String() }

func (p *browserProxy) Close() error {
	var err error
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = p.server.Shutdown(ctx)
		<-p.done
	})
	return err
}

func (p *browserProxy) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodConnect {
		p.serveConnect(response, request)
		return
	}
	if request.URL == nil || request.URL.Hostname() == "" {
		http.Error(response, "invalid proxy request", http.StatusBadRequest)
		return
	}
	target := *request.URL
	if target.Scheme == "" {
		target.Scheme = "http"
	}
	if err := validatePublicURL(request.Context(), &target, p.resolver); err != nil {
		http.Error(response, err.Error(), http.StatusForbidden)
		return
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dialPublicContext,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	outbound.URL = &target
	removeHopByHopHeaders(outbound.Header)
	result, err := transport.RoundTrip(outbound)
	if err != nil {
		http.Error(response, "proxy request failed", http.StatusBadGateway)
		return
	}
	defer result.Body.Close()
	removeHopByHopHeaders(result.Header)
	for name, values := range result.Header {
		for _, value := range values {
			response.Header().Add(name, value)
		}
	}
	response.WriteHeader(result.StatusCode)
	_, _ = io.Copy(response, result.Body)
}

func (p *browserProxy) serveConnect(response http.ResponseWriter, request *http.Request) {
	host, port, err := splitTargetAddress(request.Host, "443")
	if err != nil {
		http.Error(response, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	upstream, err := p.dialPublicHost(request.Context(), "tcp", host, port)
	if err != nil {
		http.Error(response, err.Error(), http.StatusForbidden)
		return
	}
	hijacker, ok := response.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(response, "CONNECT is unsupported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if buffered.Reader.Buffered() > 0 {
		_, _ = io.CopyN(upstream, buffered, int64(buffered.Reader.Buffered()))
	}
	go relayConnections(client, upstream)
}

func relayConnections(left, right net.Conn) {
	defer left.Close()
	defer right.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(left, right); done <- struct{}{} }()
	go func() { _, _ = io.Copy(right, left); done <- struct{}{} }()
	<-done
}

func (p *browserProxy) dialPublicContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := splitTargetAddress(address, "80")
	if err != nil {
		return nil, err
	}
	return p.dialPublicHost(ctx, network, host, port)
}

func (p *browserProxy) dialPublicHost(ctx context.Context, network, host, port string) (net.Conn, error) {
	ips, err := resolvePublicIPs(ctx, p.resolver, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		connection, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func splitTargetAddress(address, defaultPort string) (string, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err == nil {
		return host, port, nil
	}
	if strings.Contains(err.Error(), "missing port") {
		return address, defaultPort, nil
	}
	return "", "", err
}

func removeHopByHopHeaders(header http.Header) {
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

func proxyURL(raw string) (*url.URL, error) { return url.Parse(raw) }

var _ http.Hijacker = interface {
	Hijack() (net.Conn, *bufio.ReadWriter, error)
}(nil)

var _ = fmt.Sprintf
