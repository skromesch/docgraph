// Package setup downloads what the Go build needs at runtime: the ONNX model
// (+ tokenizer) and the ONNX Runtime shared library. Every artifact is pinned
// by URL, revision and SHA-256; a download that does not match is discarded.
package setup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/skromesch/docgraph/internal/embed"
)

// File is one pinned download.
type File struct {
	URL    string
	SHA256 string
	Size   int64
	Dest   string // path relative to the target directory
}

// Model is a downloadable ONNX export of an embedding model.
type Model struct {
	Name    string // HF id the index refers to (vectors are named after it)
	Backend string // onnx | onnx-fp16
	Source  string // where the export comes from (for display)
	Files   []File
}

const xenovaE5 = "https://huggingface.co/Xenova/multilingual-e5-base/resolve/1ec9243030a27d1a115d5c340572074c125b58b2/"

// Models: Xenova's exports of intfloat/multilingual-e5-base (fp32 I/O, same tokenizer).
// Verified against the PyTorch sentence-transformers vectors: fp16 min cosine 1.00000,
// same top-1 on all 52 eval queries.
var Models = []Model{
	{Name: "intfloat/multilingual-e5-base", Backend: "onnx-fp16", Source: "Xenova/multilingual-e5-base@1ec92430",
		Files: []File{
			{xenovaE5 + "onnx/model_fp16.onnx", "5d760477f691b665da2b94e1528eb6938b795f76064d9392e6af7118b8a3f54a", 555234024, "onnx/model_fp16.onnx"},
			{xenovaE5 + "tokenizer.json", "62c24cdc13d4c9952d63718d6c9fa4c287974249e16b7ade6d5a85e7bbb75626", 17082660, "tokenizer.json"},
		}},
	{Name: "intfloat/multilingual-e5-base", Backend: "onnx", Source: "Xenova/multilingual-e5-base@1ec92430",
		Files: []File{
			{xenovaE5 + "onnx/model.onnx", "84a4d426f7e87a6bf5bf195f0bae2c4a7d15f675b23ca96f42fab8326d7a77aa", 1110059084, "onnx/model.onnx"},
			{xenovaE5 + "tokenizer.json", "62c24cdc13d4c9952d63718d6c9fa4c287974249e16b7ade6d5a85e7bbb75626", 17082660, "tokenizer.json"},
		}},
}

func FindModel(name, backend string) (Model, bool) {
	if backend == "" || backend == "torch" {
		backend = "onnx-fp16"
	}
	for _, m := range Models {
		if m.Name == name && m.Backend == backend {
			return m, true
		}
	}
	return Model{}, false
}

// ModelInstalled reports whether every file of m is present with the right size.
func ModelInstalled(m Model) bool {
	dir := embed.ModelDir(m.Name)
	for _, f := range m.Files {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Dest)))
		if err != nil || st.Size() != f.Size {
			return false
		}
	}
	return true
}

// -- ONNX Runtime -----------------------------------------------------------------

const ORTVersion = "1.31.0"

type runtimeAsset struct {
	file   File
	member string // library file inside the archive
}

var runtimes = map[string]runtimeAsset{
	"darwin/arm64": {File{ortURL("onnxruntime-osx-arm64-" + ORTVersion + ".tgz"),
		"1031b36dd3bdaa8d976a6130d9cf2a05e9be320b93ae804f8427b91dc3432afc", 36549009, ""}, "libonnxruntime." + ORTVersion + ".dylib"},
	"linux/amd64": {File{ortURL("onnxruntime-linux-x64-" + ORTVersion + ".tgz"),
		"cc5c72baf5ae5c8238a6841f0897227be2d02826b9cf98eaf02fdefaeeb03a57", 11408003, ""}, "libonnxruntime.so." + ORTVersion},
	"linux/arm64": {File{ortURL("onnxruntime-linux-aarch64-" + ORTVersion + ".tgz"),
		"c5b8b3cca31f3d643a3b313b8f42e2d0f16a4fcefd02cda343d97bd5306afaab", 10331104, ""}, "libonnxruntime.so." + ORTVersion},
	"windows/amd64": {File{ortURL("onnxruntime-win-x64-" + ORTVersion + ".zip"),
		"3958d8a44984160692c1b4ffd0de3cb48f8c39f9d34bb31e433db06e30b82195", 82887349, ""}, "onnxruntime.dll"},
}

func ortURL(name string) string {
	return "https://github.com/microsoft/onnxruntime/releases/download/v" + ORTVersion + "/" + name
}

// RuntimePath is where the pulled library lives (embed.FindORTLibrary looks here).
func RuntimePath() (string, error) {
	a, ok := runtimes[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no prebuilt ONNX Runtime for %s/%s; install it and set ONNXRUNTIME_LIB", runtime.GOOS, runtime.GOARCH)
	}
	return filepath.Join(embed.LibDir(), a.member), nil
}

func RuntimeInstalled() bool {
	p, err := RuntimePath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// -- downloads --------------------------------------------------------------------

// Progress receives bytes done / total for the current file.
type Progress func(label string, done, total int64)

// PullModel downloads (or verifies) every file of m into embed.ModelDir.
func PullModel(m Model, progress Progress) error {
	dir := embed.ModelDir(m.Name)
	for _, f := range m.Files {
		dest := filepath.Join(dir, filepath.FromSlash(f.Dest))
		if ok, _ := verify(dest, f); ok {
			continue
		}
		if err := download(f, dest, path.Base(f.Dest), progress); err != nil {
			return err
		}
	}
	return nil
}

// PullRuntime downloads the ONNX Runtime archive and extracts the shared library.
func PullRuntime(progress Progress) (string, error) {
	a, ok := runtimes[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		_, err := RuntimePath()
		return "", err
	}
	target := filepath.Join(embed.LibDir(), a.member)
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}
	archive := filepath.Join(embed.LibDir(), path.Base(a.file.URL))
	if err := download(a.file, archive, "onnxruntime "+ORTVersion, progress); err != nil {
		return "", err
	}
	defer os.Remove(archive)
	if err := extractMember(archive, a.member, target); err != nil {
		return "", err
	}
	return target, nil
}

func verify(p string, f File) (bool, error) {
	fh, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer fh.Close()
	if st, _ := fh.Stat(); st.Size() != f.Size {
		return false, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == f.SHA256, nil
}

type countingWriter struct {
	done, total int64
	label       string
	progress    Progress
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.done += int64(len(p))
	if c.progress != nil {
		c.progress(c.label, c.done, c.total)
	}
	return len(p), nil
}

// download streams url to dest via dest.part, checking size and SHA-256 before the rename.
func download(f File, dest, label string, progress Progress) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	resp, err := http.Get(f.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", f.URL, resp.Status)
	}
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	cw := &countingWriter{total: f.Size, label: label, progress: progress}
	_, err = io.Copy(io.MultiWriter(out, h, cw), resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.SHA256 || cw.done != f.Size {
		os.Remove(part)
		return fmt.Errorf("download %s: checksum mismatch (got %s, %d bytes)", f.URL, got, cw.done)
	}
	return os.Rename(part, dest)
}

func extractMember(archive, member, target string) error {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if path.Base(f.Name) == member {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				defer rc.Close()
				return writeFile(target, rc)
			}
		}
		return fmt.Errorf("%s not found in %s", member, archive)
	}
	fh, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in %s", member, archive)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == member {
			return writeFile(target, tr)
		}
	}
}

func writeFile(target string, r io.Reader) error {
	tmp := target + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}
