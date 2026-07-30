package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		{"1.15.0", "1.14.58", true},
		{"v1.15.0", "1.15.0", false},
		{"1.14.57", "1.14.58", false},
		{"2.0.0", "1.99.99", true},
	}
	for _, test := range tests {
		got, err := newerVersion(test.candidate, test.current)
		if err != nil {
			t.Fatalf("%s vs %s: %v", test.candidate, test.current, err)
		}
		if got != test.want {
			t.Errorf("%s newer than %s = %v, want %v", test.candidate, test.current, got, test.want)
		}
	}
	if _, err := newerVersion("latest", "1.0.0"); err == nil {
		t.Fatal("invalid version accepted")
	}
}

func TestFetchLatestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("unexpected Accept: %q", r.Header.Get("Accept"))
		}
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v1.15.0",
			HTMLURL: "https://github.com/wad350/vless-manager/releases/tag/v1.15.0",
		})
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	release, err := fetchLatestRelease(context.Background(), client, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v1.15.0" {
		t.Fatalf("tag=%q", release.TagName)
	}
}

func TestReleaseAssetsRequireBinaryAndChecksum(t *testing.T) {
	release := githubRelease{TagName: "v1.15.0"}
	release.Assets = append(release.Assets,
		githubAsset{
			Name:               "vless-manager_1.15.0_mipsel-3.4.ipk",
			BrowserDownloadURL: "https://github.com/wad350/vless-manager/releases/download/v1.15.0/vless-manager.ipk",
			Size:               32 << 20,
		},
		githubAsset{
			Name:               "vless-manager_1.15.0_mipsel-3.4.ipk.sha256",
			BrowserDownloadURL: "https://github.com/wad350/vless-manager/releases/download/v1.15.0/vless-manager.ipk.sha256",
			Size:               100,
		},
	)
	pkg, checksum, err := releaseAssets(release, "1.15.0")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Size != 32<<20 || checksum.URL == "" {
		t.Fatalf("pkg=%+v checksum=%+v", pkg, checksum)
	}

	release.Assets = release.Assets[:1]
	if _, _, err := releaseAssets(release, "1.15.0"); err == nil {
		t.Fatal("missing checksum accepted")
	}
}

func TestDownloadVerifiedPackage(t *testing.T) {
	pkg := minimalIPK(t, "1.15.0")
	sum := sha256.Sum256(pkg)
	client := &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch req.URL.Path {
		case "/package.ipk":
			body = pkg
		case "/package.ipk.sha256":
			body = []byte(fmt.Sprintf("%x  package.ipk\n", sum))
		default:
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	destination := filepath.Join(t.TempDir(), "vless-manager.ipk")
	var phases []string
	err := downloadVerifiedPackage(
		context.Background(),
		client,
		releaseAsset{Name: "package.ipk", URL: "https://github.com/package.ipk", Size: int64(len(pkg))},
		releaseAsset{Name: "package.ipk.sha256", URL: "https://github.com/package.ipk.sha256", Size: 74},
		"1.15.0",
		destination,
		func(phase string, _, _ int64) {
			if len(phases) == 0 || phases[len(phases)-1] != phase {
				phases = append(phases, phase)
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pkg) {
		t.Fatal("downloaded package differs")
	}
	wantPhases := []string{"checksum", "downloading", "verifying", "preparing"}
	if fmt.Sprint(phases) != fmt.Sprint(wantPhases) {
		t.Fatalf("progress phases=%v, want %v", phases, wantPhases)
	}
}

func TestUpdateProgressWriterReportsBytes(t *testing.T) {
	var destination bytes.Buffer
	var reported int64
	writer := &updateProgressWriter{
		writer: &destination,
		total:  6,
		report: func(written, total int64) {
			reported = written
			if total != 6 {
				t.Errorf("total=%d", total)
			}
		},
	}
	if _, err := writer.Write([]byte("router")); err != nil {
		t.Fatal(err)
	}
	if destination.String() != "router" || reported != 6 {
		t.Fatalf("destination=%q reported=%d", destination.String(), reported)
	}
}

func TestValidateIPKRejectsWrongMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong.ipk")
	if err := os.WriteFile(path, minimalIPK(t, "9.9.9"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateIPK(path, "1.15.2"); err == nil {
		t.Fatal("IPK with wrong version accepted")
	}
}

func TestValidateBuiltIPK(t *testing.T) {
	path := os.Getenv("VLESS_MANAGER_TEST_IPK")
	version := os.Getenv("VLESS_MANAGER_TEST_IPK_VERSION")
	if path == "" || version == "" {
		t.Skip("VLESS_MANAGER_TEST_IPK is not set")
	}
	if err := validateIPK(path, version); err != nil {
		t.Fatal(err)
	}
}

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func minimalMIPSELF() []byte {
	data := make([]byte, 52)
	copy(data, []byte{0x7f, 'E', 'L', 'F'})
	data[4] = byte(1) // ELFCLASS32
	data[5] = byte(1) // ELFDATA2LSB
	data[6] = byte(1) // EV_CURRENT
	data[16] = byte(2)
	data[18] = byte(8) // EM_MIPS
	data[20] = byte(1)
	data[40] = byte(52)
	data[46] = byte(40)
	return data
}

func minimalIPK(t *testing.T, version string) []byte {
	t.Helper()
	control := []byte(fmt.Sprintf(
		"Package: vless-manager\nVersion: %s\nArchitecture: mipsel-3.4\n", version))
	controlArchive := testTarGzip(t, map[string][]byte{"./control": control})
	dataArchive := testTarGzip(t, map[string][]byte{
		"opt/bin/vless-manager": minimalMIPSELF(),
	})
	return testTarGzip(t, map[string][]byte{
		"debian-binary":  []byte("2.0\n"),
		"control.tar.gz": controlArchive,
		"data.tar.gz":    dataArchive,
	})
}

func testTarGzip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	archive := tar.NewWriter(gzipWriter)
	for name, data := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(data)),
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
