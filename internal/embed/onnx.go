package embed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// Backends and the ONNX file each one loads from the model directory.
var onnxFiles = map[string]string{
	"onnx":      "onnx/model.onnx",
	"onnx-fp16": "onnx/model_fp16.onnx",
	"onnx-int8": "onnx/model_qint8_" + quantArch() + ".onnx",
}

func quantArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "avx2"
}

const (
	MaxSeqLen = 512
	padID     = 1 // XLM-R <pad>
)

var (
	envOnce sync.Once
	envErr  error
)

// ModelDir: DOCGRAPH_MODEL_DIR or ~/.cache/docgraph/onnx, then <org>--<name>.
func ModelDir(model string) string {
	root := os.Getenv("DOCGRAPH_MODEL_DIR")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".cache", "docgraph", "onnx")
	}
	return filepath.Join(root, strings.ReplaceAll(model, "/", "--"))
}

// LibDir is where `docgraph model pull` puts the ONNX Runtime library.
func LibDir() string {
	if d := os.Getenv("DOCGRAPH_LIB_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "docgraph", "lib")
}

// FindORTLibrary locates the ONNX Runtime shared library: ONNXRUNTIME_LIB, the
// pulled copy in LibDir, next to the binary, then common install locations.
func FindORTLibrary() (string, error) {
	if p := os.Getenv("ONNXRUNTIME_LIB"); p != "" {
		return p, nil
	}
	pulled, _ := filepath.Glob(filepath.Join(LibDir(), "*onnxruntime*"))
	var candidates []string
	for _, p := range pulled {
		if !strings.HasSuffix(p, ".part") && !strings.HasSuffix(p, ".tgz") && !strings.HasSuffix(p, ".zip") {
			candidates = append(candidates, p)
		}
	}
	if exe, err := os.Executable(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), "*onnxruntime*"))
		candidates = append(candidates, matches...)
	}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, "/opt/homebrew/lib/libonnxruntime.dylib", "/usr/local/lib/libonnxruntime.dylib")
	case "windows":
		candidates = append(candidates, "onnxruntime.dll")
	default:
		candidates = append(candidates, "/usr/lib/libonnxruntime.so", "/usr/local/lib/libonnxruntime.so")
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("ONNX Runtime library not found; run `docgraph model pull` or set ONNXRUNTIME_LIB")
}

func initORT() error {
	envOnce.Do(func() {
		lib, err := FindORTLibrary()
		if err != nil {
			envErr = err
			return
		}
		ort.SetSharedLibraryPath(lib)
		envErr = ort.InitializeEnvironment()
	})
	return envErr
}

type ONNX struct {
	name      string
	dim       int
	batchSize int
	prefixes  map[Kind]string
	tok       *tokenizers.Tokenizer
	session   *ort.DynamicAdvancedSession
	mu        sync.Mutex // one session, serialized runs
}

type ONNXOptions struct {
	Model         string // HF model id, e.g. intfloat/multilingual-e5-base
	Backend       string // onnx | onnx-fp16 | onnx-int8
	Name          string // vector name; "" = NameFor(Model, Backend)
	BatchSize     int
	QueryPrefix   *string
	PassagePrefix *string
}

// NameFor: fp32 and fp16 vectors are interchangeable with each other and with
// PyTorch ones (cosine 1.0000), so they share the model name; int8 gets a suffix.
func NameFor(model, backend string) string {
	if backend == "onnx-int8" {
		return model + "#" + backend
	}
	return model
}

func NewONNX(o ONNXOptions) (*ONNX, error) {
	file, ok := onnxFiles[o.Backend]
	if !ok {
		return nil, fmt.Errorf("embedding.backend %q is not supported by the Go build (use onnx, onnx-fp16 or onnx-int8)", o.Backend)
	}
	dir := ModelDir(o.Model)
	modelPath := filepath.Join(dir, file)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("model file %s not found; run `docgraph model pull`", modelPath)
	}
	if err := initORT(); err != nil {
		return nil, err
	}
	tok, err := tokenizers.FromFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	sess, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{"input_ids", "attention_mask"}, []string{"last_hidden_state"}, opts)
	if err != nil {
		tok.Close()
		return nil, err
	}
	e := &ONNX{name: o.Name, dim: 768, batchSize: o.BatchSize, tok: tok, session: sess,
		prefixes: DefaultPrefixes(o.Model)}
	if e.name == "" {
		e.name = NameFor(o.Model, o.Backend)
	}
	if e.batchSize <= 0 {
		e.batchSize = 32
	}
	if o.QueryPrefix != nil {
		e.prefixes[Query] = *o.QueryPrefix
	}
	if o.PassagePrefix != nil {
		e.prefixes[Passage] = *o.PassagePrefix
	}
	// probe the real dimension
	v, err := e.Embed([]string{"x"}, Query)
	if err != nil {
		e.Close()
		return nil, err
	}
	e.dim = len(v[0])
	return e, nil
}

func (e *ONNX) Name() string            { return e.name }
func (e *ONNX) Dim() int                { return e.dim }
func (e *ONNX) Prefix(kind Kind) string { return e.prefixes[kind] }

func (e *ONNX) Close() error {
	e.tok.Close()
	return e.session.Destroy()
}

// tokenize: special tokens added, truncated like HF (truncation=True, max_length=512):
// content tokens are cut, the closing </s> is kept.
func (e *ONNX) tokenize(text string) []int64 {
	ids, _ := e.tok.Encode(text, true)
	if len(ids) > MaxSeqLen {
		last := ids[len(ids)-1]
		ids = append(ids[:MaxSeqLen-1], last)
	}
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out
}

func (e *ONNX) Embed(texts []string, kind Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	if len(texts) == 0 {
		return out, nil
	}
	prefix := e.prefixes[kind]
	toks := make([][]int64, len(texts))
	for i, t := range texts {
		toks[i] = e.tokenize(prefix + t)
	}
	// like sentence-transformers: batch by length to minimize padding
	order := make([]int, len(texts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(toks[order[a]]) > len(toks[order[b]]) })

	e.mu.Lock()
	defer e.mu.Unlock()
	for start := 0; start < len(order); start += e.batchSize {
		idx := order[start:min(start+e.batchSize, len(order))]
		vecs, err := e.run(toks, idx)
		if err != nil {
			return nil, err
		}
		for j, i := range idx {
			out[i] = vecs[j]
		}
	}
	return out, nil
}

func (e *ONNX) run(toks [][]int64, idx []int) ([][]float32, error) {
	b := len(idx)
	seq := 0
	for _, i := range idx {
		seq = max(seq, len(toks[i]))
	}
	ids := make([]int64, b*seq)
	mask := make([]int64, b*seq)
	for r, i := range idx {
		for c := 0; c < seq; c++ {
			if c < len(toks[i]) {
				ids[r*seq+c] = toks[i][c]
				mask[r*seq+c] = 1
			} else {
				ids[r*seq+c] = padID
			}
		}
	}
	shape := ort.NewShape(int64(b), int64(seq))
	idsT, err := ort.NewTensor(shape, ids)
	if err != nil {
		return nil, err
	}
	defer idsT.Destroy()
	maskT, err := ort.NewTensor(shape, mask)
	if err != nil {
		return nil, err
	}
	defer maskT.Destroy()
	outputs := []ort.Value{nil}
	if err := e.session.Run([]ort.Value{idsT, maskT}, outputs); err != nil {
		return nil, err
	}
	defer outputs[0].Destroy()
	hidden, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("unexpected output type %T", outputs[0])
	}
	data := hidden.GetData()
	dim := int(hidden.GetShape()[2])
	res := make([][]float32, b)
	for r := 0; r < b; r++ {
		// mean pooling over real tokens, accumulated in float64
		acc := make([]float64, dim)
		n := 0
		for c := 0; c < seq; c++ {
			if mask[r*seq+c] == 0 {
				continue
			}
			n++
			row := data[(r*seq+c)*dim : (r*seq+c+1)*dim]
			for k, x := range row {
				acc[k] += float64(x)
			}
		}
		v := make([]float32, dim)
		for k := range v {
			v[k] = float32(acc[k] / float64(max(n, 1)))
		}
		Normalize(v)
		res[r] = v
	}
	return res, nil
}
