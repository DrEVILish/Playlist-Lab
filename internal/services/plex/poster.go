package plex

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"time"
)

const maxPosterBytes = 10 << 20

var imagePath = regexp.MustCompile(`^/(?:library/metadata|playlists)/[a-zA-Z0-9_-]+/(?:thumb|art|composite)(?:/[0-9]+)?$`)

func IsImagePath(path string) bool {
	return imagePath.MatchString(path)
}

func RasterContentType(data []byte) string {
	switch contentType := http.DetectContentType(data); contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/x-icon":
		return contentType
	default:
		return ""
	}
}

var (
	// These special-use ranges are not rejected by IsPrivate.
	posterSpecialRanges = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2002::/16"),
	}
	posterHTTPClient = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialPublicPoster,
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        16,
			TLSHandshakeTimeout: 5 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many poster redirects")
			}
			return checkPosterURL(req.URL)
		},
	}
)

func checkPosterURL(address *url.URL) error {
	if (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" || address.User != nil {
		return fmt.Errorf("poster URL must be a public HTTP or HTTPS address")
	}
	return nil
}

func isPublicPosterAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return false
	}
	for _, reserved := range posterSpecialRanges {
		if reserved.Contains(address) {
			return false
		}
	}
	return true
}

// Connect to the address that passed validation. A second DNS lookup could
// change which address the socket connects to.
func dialPublicPoster(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	lastErr := fmt.Errorf("poster host has no permitted public address")
	for _, address := range addresses {
		if !isPublicPosterAddress(address) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func ReadPoster(r io.Reader) ([]byte, string, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxPosterBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxPosterBytes {
		return nil, "", fmt.Errorf("poster exceeds the 10 MB limit")
	}
	contentType := RasterContentType(body)
	if contentType == "" {
		return nil, "", fmt.Errorf("poster must be a supported raster image")
	}
	return body, contentType, nil
}

func (c *Client) UploadPlaylistPoster(playlistID, imageURL string) error {
	if IsImagePath(imageURL) {
		return c.CopyPlaylistPosterTo(c, playlistID, imageURL)
	}
	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return fmt.Errorf("invalid poster URL")
	}
	if err := checkPosterURL(req.URL); err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Playlist Lab/1.0")
	resp, err := posterHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download poster from a public address")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download poster image: status %d", resp.StatusCode)
	}
	return c.uploadPoster(playlistID, resp.Body)
}

func (c *Client) CopyPlaylistPosterTo(target *Client, playlistID, path string) error {
	body, _, err := c.FetchMedia(path)
	if err != nil {
		return err
	}
	defer body.Close()
	return target.uploadPoster(playlistID, body)
}

func (c *Client) uploadPoster(playlistID string, body io.Reader) error {
	poster, contentType, err := ReadPoster(body)
	if err != nil {
		return err
	}
	return c.UploadPlaylistPosterBytes(playlistID, poster, contentType)
}
