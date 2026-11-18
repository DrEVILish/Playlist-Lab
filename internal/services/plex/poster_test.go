package plex

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
)

func TestReadPosterAcceptsBoundedRasterImage(t *testing.T) {
	const png = "\x89PNG\r\n\x1a\ncover"
	got, contentType, err := ReadPoster(strings.NewReader(png))
	if err != nil || string(got) != png || contentType != "image/png" {
		t.Fatalf("expected bounded image bytes and detected type, got %q, %q, %v", got, contentType, err)
	}
	if _, _, err := ReadPoster(strings.NewReader("Thumbnail not ready.")); err == nil {
		t.Fatal("expected the incomplete thumbnail to be rejected")
	}
	if _, _, err := ReadPoster(bytes.NewReader(make([]byte, maxPosterBytes+1))); err == nil {
		t.Fatal("expected the oversized thumbnail to be rejected")
	}
}

func TestPosterAddressPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.7.0.1", "192.168.1.1", "100.64.0.1", "::1", "fe80::1", "fd12::1", "::ffff:192.168.1.1"} {
		if isPublicPosterAddress(netip.MustParseAddr(ip)) {
			t.Errorf("private or reserved address %s is not a public poster host", ip)
		}
	}
	if !isPublicPosterAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public unicast address was rejected")
	}
}
