package tools

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestResolvePublicIPsRejectsPrivateDNSResult(t *testing.T) {
	resolver := staticResolver{"example.test": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}}
	if _, err := resolvePublicIPs(context.Background(), resolver, "example.test"); err == nil {
		t.Fatal("expected private DNS result rejection")
	}
}

func TestBrowserProxyRejectsPrivateConnectTarget(t *testing.T) {
	proxy := &browserProxy{resolver: staticResolver{"private.test": {netip.MustParseAddr("10.0.0.1")}}}
	if _, err := proxy.dialPublicHost(context.Background(), "tcp", "private.test", "443"); err == nil {
		t.Fatal("expected private CONNECT target rejection")
	}
}

func TestBrowserProxyRejectsPrivateHTTPRequest(t *testing.T) {
	proxy, err := newBrowserProxy(staticResolver{"private.test": {netip.MustParseAddr("10.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyAddress, _ := url.Parse(proxy.URL())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyAddress)}}
	response, err := client.Get("http://private.test/resource")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestBrowserProxyRejectsPrivateCONNECTRequest(t *testing.T) {
	proxy, err := newBrowserProxy(staticResolver{"private.test": {netip.MustParseAddr("10.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("CONNECT private.test:443 HTTP/1.1\r\nHost: private.test:443\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}
