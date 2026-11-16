//go:build ignore

// fetch_wgpu downloads the pinned wgpu-native release for this platform into wgpu-native/<goos>_<goarch>/
// (the C headers and the static library), checking the archive's SHA-256. `go generate ./shader/gpu` runs
// it; the folder is not committed.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const version = "v29.0.1.1"

// The release archives, by GOOS_GOARCH, with their SHA-256. Windows uses the MinGW (gnu) build.
var assets = map[string][2]string{
	"windows_amd64": {"wgpu-windows-x86_64-gnu-release.zip", "d471e3614733c1d4ddd61bfd19868356477d0d37bf531bf8c6cb64a7f579bd2a"},
	"linux_amd64":   {"wgpu-linux-x86_64-release.zip", "95a4d90c071005a98d03eab348beaa6b07e16eb00d1dcdb9f8348f75eb97ec5a"},
}

// The archive members kept, and where they go under the platform folder.
var keep = map[string]string{
	"include/webgpu/webgpu.h": "include/webgpu/webgpu.h",
	"include/webgpu/wgpu.h":   "include/webgpu/wgpu.h",
	"lib/libwgpu_native.a":    "lib/libwgpu_native.a",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fetch_wgpu:", err)
		os.Exit(1)
	}
}

func run() error {
	plat := runtime.GOOS + "_" + runtime.GOARCH
	asset, ok := assets[plat]
	if !ok {
		return fmt.Errorf("no pinned wgpu-native build for %s", plat)
	}
	dir := filepath.Join("wgpu-native", plat)
	stamp := filepath.Join(dir, "VERSION")
	want := version + " " + asset[1] + "\n"
	if b, err := os.ReadFile(stamp); err == nil && string(b) == want {
		return nil
	}
	url := "https://github.com/gfx-rs/wgpu-native/releases/download/" + version + "/" + asset[0]
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != asset[1] {
		return fmt.Errorf("%s: SHA-256 %x, want %s", asset[0], sum, asset[1])
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	found := 0
	for _, f := range zr.File {
		to, ok := keep[strings.TrimPrefix(f.Name, "./")]
		if !ok {
			continue
		}
		if err := extract(f, filepath.Join(dir, filepath.FromSlash(to))); err != nil {
			return err
		}
		found++
	}
	if found != len(keep) {
		return fmt.Errorf("%s: %d of %d expected files", asset[0], found, len(keep))
	}
	return os.WriteFile(stamp, []byte(want), 0o644)
}

func extract(f *zip.File, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
