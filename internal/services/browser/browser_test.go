package browser

import "testing"

func TestProviderURLs(t *testing.T) {
	for _, test := range []struct {
		url  string
		want bool
	}{
		{"https://music.apple.com/us/playlist/example", true},
		{"https://is1-ssl.mzstatic.com/image/example", true},
		{"https://example.org/", false},
		{"http://music.apple.com/us/playlist/example", false},
	} {
		if got := allowedURL(test.url, []string{"apple.com", "mzstatic.com"}); got != test.want {
			t.Errorf("allow %q = %v, want %v", test.url, got, test.want)
		}
	}
}
