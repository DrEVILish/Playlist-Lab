package deemix

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadRejectsEmptyOrTruncatedTrack(t *testing.T) {
	for _, tc := range []struct {
		name, length, body string
		wantError          bool
	}{
		{name: "empty", length: "0", wantError: true},
		{name: "short response", length: "2048", body: "short", wantError: true},
		{name: "complete partial final block", length: "5", body: "audio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", tc.length)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			var out bytes.Buffer
			err := downloadAndDecrypt(server.URL, "123", &out, nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("download error = %v, want error: %t", err, tc.wantError)
			}
			if !tc.wantError && out.String() != tc.body {
				t.Fatalf("downloaded %q, want %q", out.String(), tc.body)
			}
		})
	}
}

func TestInstallTrackLeavesExistingFileUnlessExplicitlyOverwriting(t *testing.T) {
	for _, mode := range []string{"", OverwriteNo, OverwriteKeepBoth, OverwriteOnlyTags, OverwriteDontCheckExt, OverwriteYes} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "track.mp3")
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			installed, err := installTrack(path, mode, func(file *os.File) error {
				_, err := file.WriteString("new track")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "original"
			if mode == OverwriteYes {
				want = "new track"
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != want || installed != (mode == OverwriteYes) {
				t.Fatalf("file = %q, error = %v, installed = %t, want %q", data, err, installed, want)
			}
		})
	}
}

func TestInstallTrackHandlesConcurrentWritersWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	firstStarted := make(chan struct{})
	firstMayPublish := make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		installed, err := installTrack(path, OverwriteNo, func(file *os.File) error {
			close(firstStarted)
			<-firstMayPublish
			_, err := file.WriteString("first")
			return err
		})
		if err == nil && installed {
			err = fmt.Errorf("first writer should have kept the existing file")
		}
		firstResult <- err
	}()
	<-firstStarted
	installed, err := installTrack(path, OverwriteNo, func(file *os.File) error {
		_, err := file.WriteString("second")
		return err
	})
	close(firstMayPublish)
	if err != nil || !installed {
		t.Fatalf("second write = %t, %v", installed, err)
	}
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatalf("file = %q, error = %v, want second", data, err)
	}
}
