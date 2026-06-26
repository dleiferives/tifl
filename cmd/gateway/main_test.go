package main

import (
	"net"
	"net/url"
	"testing"
)

func TestHTTPURLIncludesRandomPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	u, err := url.Parse(httpURL(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Port() == "0" {
		t.Fatalf("httpURL(%q) = %q", ln.Addr().String(), u.String())
	}
}
