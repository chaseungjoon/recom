package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

var testSeed = bytes.Repeat([]byte{7}, ed25519.SeedSize)

func testKey() ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey)
}

// releaseServer serves /releases/latest → tag and the tag's assets.
func releaseServer(t *testing.T, tag string, archive []byte, corrupt bool) *httptest.Server {
	u := &Updater{OS: "linux", Arch: "amd64"}
	name := u.Archive(tag)
	sum := sha256.Sum256(archive)
	if corrupt {
		sum[0] ^= 0xff
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	sums := fmt.Sprintf("%x  %s\n%x  other.tar.gz\n", sum, name, sha256.Sum256(nil))
	mux.HandleFunc("/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sums))
	})
	sig, err := Sign(testSeed, []byte(sums))
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("/releases/download/"+tag+"/"+SignatureAsset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(sig)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+name, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateFlow(t *testing.T) {
	script := "#!/bin/sh\necho 'recom 9.8.7'\n"
	archive := tarball(t, map[string]string{"LICENSE": "MIT", "recom": script})
	srv := releaseServer(t, "v9.8.7", archive, false)
	u := &Updater{BaseURL: srv.URL + "/releases", OS: "linux", Arch: "amd64", Client: srv.Client(), PublicKey: testKey()}

	tag, err := u.Latest(context.Background())
	if err != nil || tag != "v9.8.7" {
		t.Fatalf("latest = %q, %v", tag, err)
	}
	bin, err := u.Fetch(context.Background(), tag)
	if err != nil {
		t.Fatal(err)
	}
	if string(bin) != script {
		t.Fatalf("extracted %q", bin)
	}
	exe := filepath.Join(t.TempDir(), "recom")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Install(exe, bin, "9.8.7"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != script {
		t.Fatalf("binary not replaced: %q", b)
	}
	// A binary that reports the wrong version is not installed.
	if err := Install(exe, []byte("#!/bin/sh\necho 'recom 1.0.0'\n"), "9.8.7"); err == nil || !strings.Contains(err.Error(), "expected version") {
		t.Fatalf("version check: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != script {
		t.Fatal("failed install touched the binary")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".recom-update-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestChecksumMismatchIsRejected(t *testing.T) {
	archive := tarball(t, map[string]string{"recom": "x"})
	srv := releaseServer(t, "v1.2.3", archive, true)
	u := &Updater{BaseURL: srv.URL + "/releases", OS: "linux", Arch: "amd64", Client: srv.Client(), PublicKey: testKey()}
	if _, err := u.Fetch(context.Background(), "v1.2.3"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	u.OS = "plan9"
	if _, err := u.Fetch(context.Background(), "v1.2.3"); err == nil || !strings.Contains(err.Error(), "unsupported platform") {
		t.Fatalf("err = %v", err)
	}
}

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"0.1.0", "v0.2.0", true},
		{"v0.2.0-3-gabcdef-dirty", "0.2.0", false},
		{"0.10.0", "0.9.9", false},
		{"1.0.0", "1.0.1", true},
	} {
		a, ok1 := Parse(c.a)
		b, ok2 := Parse(c.b)
		if !ok1 || !ok2 || a.Less(b) != c.less {
			t.Errorf("%s < %s = %v, want %v", c.a, c.b, a.Less(b), c.less)
		}
	}
	if _, ok := Parse("dev"); ok {
		t.Error("dev must not parse")
	}
	if ManagedBy("/opt/homebrew/Cellar/recom/0.1.0/bin/recom") != "Homebrew" || ManagedBy("/usr/local/bin/recom") != "" {
		t.Error("ManagedBy")
	}
}

func TestSignatureIsRequired(t *testing.T) {
	archive := tarball(t, map[string]string{"recom": "x"})
	srv := releaseServer(t, "v1.2.3", archive, false)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	u := &Updater{BaseURL: srv.URL + "/releases", OS: "linux", Arch: "amd64", Client: srv.Client(), PublicKey: other}
	if _, err := u.Fetch(context.Background(), "v1.2.3"); err == nil || !strings.Contains(err.Error(), "signature is invalid") {
		t.Fatalf("wrong key accepted: %v", err)
	}
	// Releases from FirstSigned on must have a signature at all.
	unsigned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			fmt.Fprintf(w, "%x  recom_0.2.4_linux_amd64.tar.gz\n", sha256.Sum256(archive))
			return
		}
		http.NotFound(w, r)
	}))
	defer unsigned.Close()
	u.BaseURL = unsigned.URL + "/releases"
	if _, err := u.Fetch(context.Background(), "v0.2.4"); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("unsigned release accepted: %v", err)
	}
	if err := Verify(nil, []byte("x"), []byte("bm9wZQ==")); err == nil {
		t.Fatal("garbage signature accepted by the release key")
	}
	if needsSignature("v0.2.3") || !needsSignature("v0.2.4") || !needsSignature("nightly") {
		t.Fatal("needsSignature cut-over")
	}
}
