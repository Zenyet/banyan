package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		release, current string
		want             bool
	}{
		{"0.4.2", "0.4.1", true},
		{"0.5.0", "0.4.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.4.1", "0.4.1", false},
		{"0.4.0", "0.4.1", false},
		{"v0.4.2", "0.4.1", true},
		{"0.4.2-beta", "0.4.1", true},
		{"nonsense", "0.4.1", false},
	}
	for _, c := range cases {
		if got := newer(c.release, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.release, c.current, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Manifest{
			Version:  "0.4.2",
			NotesURL: "https://example.test/notes",
			Platforms: map[string]Asset{
				Platform(): {URL: "https://example.test/download", SHA256: "abc", Size: 3},
			},
		})
	}))
	defer server.Close()
	old := ManifestURL
	ManifestURL = server.URL
	defer func() { ManifestURL = old }()

	m, err := Check(context.Background(), "0.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Version != "0.4.2" {
		t.Fatalf("manifest = %+v", m)
	}
	if a, ok := m.Asset(); !ok || a.Size != 3 {
		t.Errorf("asset = %+v, %v", a, ok)
	}
	if m, err := Check(context.Background(), "0.4.2"); err != nil || m != nil {
		t.Errorf("same version: %+v, %v", m, err)
	}
	if m, err := Check(context.Background(), "dev"); err != nil || m != nil {
		t.Errorf("dev: %+v, %v", m, err)
	}
}

func TestDownloadChecksum(t *testing.T) {
	body := []byte("the update")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer server.Close()
	sum := sha256.Sum256(body)
	dest := filepath.Join(t.TempDir(), "asset")
	a := Asset{URL: server.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	if err := download(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(body) {
		t.Errorf("downloaded %q", data)
	}
	a.SHA256 = strings.Repeat("0", 64)
	if err := download(context.Background(), a, dest); err == nil {
		t.Error("accepted the wrong checksum")
	}
}
