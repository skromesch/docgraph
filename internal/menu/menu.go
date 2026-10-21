package menu

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/index"
	"github.com/skromesch/docgraph/internal/search"
	"github.com/skromesch/docgraph/internal/setup"
	"github.com/skromesch/docgraph/internal/store"
)

// Deps are the pieces the menu borrows from the CLI.
type Deps struct {
	NewEmbedder func(config.Config) (embed.Embedder, error)
	Exe         string // absolute path of this binary (for the MCP registration command)
}

type menu struct {
	ui   *ui
	deps Deps
	err  io.Writer
	root string // project root ("" = no project yet)
	cfg  config.Config
}

// Run shows the interactive menu; it returns the process exit code.
func Run(in io.Reader, out, errOut io.Writer, plain bool, deps Deps) int {
	plain = plain || os.Getenv("DOCGRAPH_PLAIN") != "" || os.Getenv("TERM") == "dumb" || !StdinIsTerminal()
	m := &menu{ui: &ui{in: newEOFReader(in), out: out, plain: plain}, deps: deps, err: errOut}
	if root, err := config.FindRoot("."); err == nil {
		m.root = root
	}
	err := m.loop()
	switch {
	case err == nil:
		return 0
	case errors.Is(err, huh.ErrUserAborted):
		fmt.Fprintln(errOut, "\naborted")
		return 130
	default:
		fmt.Fprintln(errOut, "docgraph:", err)
		return 1
	}
}

func (m *menu) loop() error {
	for {
		opts, err := m.status()
		if err != nil {
			return err
		}
		v, err := m.ui.selectOne("What next?", "Ctrl+C quits here; inside a flow it goes back to this menu", opts)
		if err != nil {
			return err
		}
		switch v {
		case "init":
			err = m.initProject()
		case "index":
			err = m.indexFolder()
		case "sync":
			err = m.sync()
		case "rebuild":
			err = m.rebuild()
		case "ignore":
			err = m.ignoreMenu()
		case "model":
			err = m.modelMenu()
		case "search":
			err = m.trySearch()
		case "register":
			err = m.register()
		case "quit":
			return nil
		}
		if err != nil {
			switch {
			case errors.Is(err, errBack):
			case errors.Is(err, huh.ErrUserAborted):
				m.ui.printf("\n(cancelled, back at the menu)\n")
			case errors.Is(err, errInputClosed):
				return err
			default:
				m.ui.printf("\n✗ %v\n", err)
			}
		}
	}
}

// status prints the header and returns the menu options that make sense now.
func (m *menu) status() ([]choice, error) {
	m.ui.printf("\n")
	if m.root != "" {
		cfg, err := config.Load(m.root)
		if err != nil {
			return nil, err
		}
		m.cfg = cfg
	}
	modelOK, runtimeOK := m.modelState()
	if m.root == "" {
		cwd, _ := os.Getwd()
		m.ui.printf("No DocGraph project here (%s).\n", cwd)
		m.ui.printf("Model: %s   ONNX Runtime: %s\n\n", okStr(modelOK), okStr(runtimeOK))
		return []choice{
			{"Initialize a project here", "init"},
			{"Model and ONNX Runtime: status / download", "model"},
			{"Quit", "quit"},
		}, nil
	}
	cfg := m.cfg
	db, err := store.Open(config.DBPath(m.root))
	if err != nil {
		return nil, err
	}
	s, err := db.Stats()
	db.Close()
	if err != nil {
		return nil, err
	}
	m.ui.printf("Project: %s\n", m.root)
	if s.SourceRoot == "" {
		m.ui.printf("Docs folder: (not indexed yet)\n")
	} else {
		lines, _ := index.ReadIgnoreFile(s.SourceRoot)
		m.ui.printf("Docs folder: %s\n", s.SourceRoot)
		m.ui.printf("Index: %d documents, %d chunks (%d embedded)   ignore rules: %d\n",
			s.Documents, s.Chunks, s.ChunksEmbedded, len(index.Patterns(lines))+len(index.Patterns(cfg.Index.Ignore)))
	}
	m.ui.printf("Model: %s   ONNX Runtime: %s\n\n", okStr(modelOK), okStr(runtimeOK))

	var opts []choice
	if s.SourceRoot == "" {
		opts = append(opts, choice{"Index a docs folder", "index"})
	} else {
		opts = append(opts,
			choice{"Sync: re-index changed files", "sync"},
			choice{"Ignore rules (.docgraphignore): show, add, remove", "ignore"},
			choice{"Try a search", "search"},
			choice{"Rebuild the index from scratch", "rebuild"})
	}
	opts = append(opts,
		choice{"Model and ONNX Runtime: status / download", "model"},
		choice{"Register the MCP server with Claude Code", "register"},
		choice{"Quit", "quit"})
	return opts, nil
}

func okStr(ok bool) string {
	if ok {
		return "✓ installed"
	}
	return "✗ missing"
}

func (m *menu) modelState() (model, runtime bool) {
	cfg := m.cfg
	if m.root == "" {
		cfg = config.Default()
	}
	if mdl, ok := setup.FindModel(cfg.Embedding.Model, cfg.Embedding.Backend); ok {
		model = setup.ModelInstalled(mdl)
	}
	if _, err := embed.FindORTLibrary(); err == nil {
		runtime = true
	}
	return model, runtime
}

// -- project ----------------------------------------------------------------------

func (m *menu) initProject() error {
	cwd, _ := os.Getwd()
	ok, err := m.ui.confirm(fmt.Sprintf("Create %s here?", filepath.Join(cwd, config.Dir)), true)
	if err != nil || !ok {
		return err
	}
	if _, err := config.Save(cwd, config.Default()); err != nil {
		return err
	}
	db, err := store.Open(config.DBPath(cwd))
	if err != nil {
		return err
	}
	db.Close()
	m.root = cwd
	m.ui.printf("✓ initialized %s\n", config.ProjectDir(cwd))
	return m.indexFolder()
}

func (m *menu) indexFolder() error {
	def := ""
	for _, c := range []string{"wiki", "docs", "."} {
		if st, err := os.Stat(filepath.Join(m.root, c)); err == nil && st.IsDir() {
			def = c
			break
		}
	}
	dir, err := m.ui.input("Which folder holds the documentation?", "relative to "+m.root, def, func(s string) error {
		p := s
		if !filepath.IsAbs(p) {
			p = filepath.Join(m.root, p)
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return fmt.Errorf("not a folder: %s", s)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(m.root, dir)
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	// rules go in before the first index, so excluded files are never embedded
	if err := m.ignoreEditor(dir, true); err != nil {
		return err
	}
	return m.runIndex(dir, false)
}

// ensureModel offers the download when the model or runtime is missing; false = keyword only.
func (m *menu) ensureModel() (embed.Embedder, error) {
	if model, rt := m.modelState(); !model || !rt {
		ok, err := m.ui.confirm("The embedding model / ONNX Runtime is missing. Download it now (~600 MB)?", true)
		if err != nil {
			return nil, err
		}
		if !ok {
			m.ui.printf("Continuing with keyword search only; a later sync embeds the backlog.\n")
			return nil, nil
		}
		if err := m.pull(); err != nil {
			return nil, err
		}
	}
	m.ui.printf("Loading the model ...\n")
	return m.deps.NewEmbedder(m.cfg)
}

func (m *menu) runIndex(dir string, quick bool) error {
	cfg, err := config.Load(m.root)
	if err != nil {
		return err
	}
	m.cfg = cfg
	e, err := m.ensureModel()
	if err != nil {
		return err
	}
	if e != nil {
		defer e.Close()
	}
	db, err := store.Open(config.DBPath(m.root))
	if err != nil {
		return err
	}
	defer db.Close()
	last := -1
	s, err := index.New(db, cfg, e, chunk.GetCounter(cfg.Embedding.Model)).Index(dir, func(done, total int, label string) {
		if pct := done * 100 / max(total, 1); pct/10 != last/10 || done == total {
			last = pct
			m.ui.printf("  %s %d/%d\n", map[bool]string{true: "embedding", false: "files"}[label == "embedding"], done, total)
		}
	}, quick)
	if err != nil {
		return err
	}
	m.ui.printf("✓ %d documents: %d new, %d modified, %d deleted, %d unchanged; %d chunks embedded (%d reused) in %.1fs\n",
		s.Scanned, s.New, s.Modified, s.Deleted, s.Unchanged, s.ChunksEmbedded, s.EmbeddingsReused, s.Seconds)
	for _, e := range s.Errors {
		m.ui.printf("  error: %s\n", e)
	}
	return nil
}

func (m *menu) sourceRoot() (string, error) {
	db, err := store.Open(config.DBPath(m.root))
	if err != nil {
		return "", err
	}
	defer db.Close()
	return db.GetMeta("source_root")
}

func (m *menu) sync() error {
	src, err := m.sourceRoot()
	if err != nil {
		return err
	}
	return m.runIndex(src, true)
}

func (m *menu) rebuild() error {
	ok, err := m.ui.confirm("Delete the index and build it again (embeddings are recomputed)?", false)
	if err != nil || !ok {
		return err
	}
	src, err := m.sourceRoot()
	if err != nil {
		return err
	}
	for _, suf := range []string{"", "-wal", "-shm"} {
		os.Remove(config.DBPath(m.root) + suf)
	}
	return m.runIndex(src, false)
}

// -- ignore rules -----------------------------------------------------------------

func (m *menu) ignoreMenu() error {
	src, err := m.sourceRoot()
	if err != nil {
		return err
	}
	return m.ignoreEditor(src, false)
}

// ignoreEditor edits <src>/.docgraphignore. In setup mode (before the first
// index) the exit option continues to indexing and saving does not offer a sync.
func (m *menu) ignoreEditor(src string, setup bool) error {
	cfg, err := config.Load(m.root)
	if err != nil {
		return err
	}
	m.cfg = cfg
	ic := m.cfg.Index
	for {
		lines, err := index.ReadIgnoreFile(src)
		if err != nil {
			return err
		}
		kept, excluded, err := index.PreviewIgnore(src, ic.Extensions, ic.ExcludeDirs, ic.Ignore, lines)
		if err != nil {
			return err
		}
		m.ui.printf("\n%s\n", filepath.Join(src, index.IgnoreFile))
		pats := index.Patterns(lines)
		if len(pats) == 0 {
			m.ui.printf("  (no rules)\n")
		}
		for _, p := range pats {
			m.ui.printf("  %s\n", p)
		}
		if slices.Equal(ic.Ignore, config.AgentIgnore) {
			m.ui.printf("config.yaml index.ignore: AI agent files (CLAUDE.md, AGENTS.md, .claude/, .cursor/, … %d patterns; \"!name\" here re-includes)\n", len(ic.Ignore))
		} else if len(ic.Ignore) > 0 {
			m.ui.printf("config.yaml index.ignore: %s\n", strings.Join(ic.Ignore, ", "))
		}
		m.ui.printf("%d files indexed, %d excluded\n\n", kept, len(excluded))

		opts := []choice{{"Add a rule (preview before saving)", "add"}}
		if len(pats) > 0 {
			opts = append(opts, choice{"Remove rules", "remove"})
		}
		if len(excluded) > 0 {
			opts = append(opts, choice{"Show excluded files", "show"})
		}
		if setup {
			opts = append([]choice{{fmt.Sprintf("Continue: index %d files", kept), "back"}}, opts...)
		} else {
			opts = append(opts, choice{"« back to menu", "back"})
		}
		v, err := m.ui.selectOne("Ignore rules", "gitignore syntax: dir/  *.txt  **/Generated/  !keep.md  /root-only.md", opts)
		if err != nil {
			return err
		}
		switch v {
		case "back":
			return nil
		case "show":
			for _, p := range excluded {
				m.ui.printf("  %s\n", p)
			}
		case "add":
			if err := m.addIgnore(src, lines, len(excluded), setup); err != nil && !errors.Is(err, errBack) {
				return err
			}
		case "remove":
			if err := m.removeIgnore(src, lines, len(excluded), setup); err != nil && !errors.Is(err, errBack) {
				return err
			}
		}
	}
}

func (m *menu) addIgnore(src string, lines []string, before int, setup bool) error {
	pat, err := m.ui.input("New rule", "e.g. **/GeneratedDocs/  or  Archive/  or  *.txt  (! re-includes)", "", func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("enter a pattern")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return m.saveIgnore(src, append(append([]string{}, lines...), pat), before, setup)
}

func (m *menu) removeIgnore(src string, lines []string, before int, setup bool) error {
	var opts []choice
	for _, p := range index.Patterns(lines) {
		opts = append(opts, choice{p, p})
	}
	drop, err := m.ui.multiSelect("Remove which rules?", "space selects, enter confirms", opts)
	if err != nil {
		return err
	}
	if len(drop) == 0 {
		return errBack
	}
	gone := map[string]bool{}
	for _, d := range drop {
		gone[d] = true
	}
	var keep []string
	for _, l := range lines {
		if !gone[l] {
			keep = append(keep, l)
		}
	}
	return m.saveIgnore(src, keep, before, setup)
}

// saveIgnore previews the effect, asks, writes the file and offers a sync.
func (m *menu) saveIgnore(src string, lines []string, before int, setup bool) error {
	ic := m.cfg.Index
	kept, excluded, err := index.PreviewIgnore(src, ic.Extensions, ic.ExcludeDirs, ic.Ignore, lines)
	if err != nil {
		return err
	}
	m.ui.printf("\nWith this change: %d files indexed, %d excluded (now %d).\n", kept, len(excluded), before)
	for i, p := range excluded {
		if i == 15 {
			m.ui.printf("  ... %d more\n", len(excluded)-15)
			break
		}
		m.ui.printf("  - %s\n", p)
	}
	ok, err := m.ui.confirm("Save .docgraphignore?", true)
	if err != nil || !ok {
		return err
	}
	if err := index.WriteIgnoreFile(src, lines); err != nil {
		return err
	}
	if setup {
		m.ui.printf("✓ saved.\n")
		return nil
	}
	m.ui.printf("✓ saved. The MCP server applies it on its next sync.\n")
	if ok, err := m.ui.confirm("Sync now?", true); err != nil || !ok {
		return err
	}
	return m.runIndex(src, true)
}

// -- model ------------------------------------------------------------------------

func (m *menu) modelMenu() error {
	cfg := m.cfg
	if m.root == "" {
		cfg = config.Default()
	}
	mdl, ok := setup.FindModel(cfg.Embedding.Model, cfg.Embedding.Backend)
	m.ui.printf("\nModel: %s (%s)\n", cfg.Embedding.Model, cfg.Embedding.Backend)
	if !ok {
		m.ui.printf("  no download available for this model/backend; place it in %s\n", embed.ModelDir(cfg.Embedding.Model))
	} else {
		m.ui.printf("  %s  %s, from %s\n", okStr(setup.ModelInstalled(mdl)), embed.ModelDir(mdl.Name), mdl.Source)
	}
	if lib, err := embed.FindORTLibrary(); err == nil {
		m.ui.printf("ONNX Runtime: ✓ %s\n", lib)
	} else {
		m.ui.printf("ONNX Runtime: ✗ missing (download: %s)\n", setup.ORTVersion)
	}
	model, rt := m.modelState()
	if model && rt {
		return nil
	}
	ok2, err := m.ui.confirm("Download what is missing now?", true)
	if err != nil || !ok2 {
		return err
	}
	return m.pull()
}

func (m *menu) pull() error {
	cfg := m.cfg
	if m.root == "" {
		cfg = config.Default()
	}
	return Pull(cfg, m.ui.out)
}

// Pull downloads the configured model and the ONNX Runtime, printing progress.
func Pull(cfg config.Config, out io.Writer) error {
	last := map[string]int64{}
	progress := func(label string, done, total int64) {
		pct := done * 100 / max(total, 1)
		if pct/5 != last[label]/5 || done == total {
			last[label] = pct
			fmt.Fprintf(out, "\r  %-28s %3d%%  %6.1f / %.1f MB", label, pct, float64(done)/1e6, float64(total)/1e6)
			if done == total {
				fmt.Fprintln(out)
			}
		}
	}
	if _, err := embed.FindORTLibrary(); err != nil {
		p, err := setup.PullRuntime(progress)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ ONNX Runtime %s: %s\n", setup.ORTVersion, p)
	}
	mdl, ok := setup.FindModel(cfg.Embedding.Model, cfg.Embedding.Backend)
	if !ok {
		return fmt.Errorf("no download available for %s (%s)", cfg.Embedding.Model, cfg.Embedding.Backend)
	}
	if err := setup.PullModel(mdl, progress); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ model %s (%s): %s\n", mdl.Name, mdl.Backend, embed.ModelDir(mdl.Name))
	return nil
}

// -- search & register ------------------------------------------------------------

func (m *menu) trySearch() error {
	q, err := m.ui.input("Search", "a question or an identifier", "", nil)
	if err != nil || q == "" {
		return err
	}
	db, err := store.Open(config.DBPath(m.root))
	if err != nil {
		return err
	}
	defer db.Close()
	bm25 := &search.BM25{DB: db, Cfg: m.cfg.Search.BM25}
	run := bm25.Search
	if model, rt := m.modelState(); model && rt {
		e, err := m.deps.NewEmbedder(m.cfg)
		if err != nil {
			return err
		}
		defer e.Close()
		vec, err := search.NewVector(db, e, store.SqliteVec{DB: db})
		if err != nil {
			return err
		}
		run = (&search.Hybrid{BM25: bm25, Vector: vec, Cfg: m.cfg.Search}).Search
	} else {
		m.ui.printf("(keyword search: the model is not installed)\n")
	}
	res, err := run(q, 5)
	if err != nil {
		return err
	}
	if len(res) == 0 {
		m.ui.printf("No results.\n")
	}
	for _, r := range res {
		text := []rune(strings.Join(strings.Fields(r.Content), " "))
		if len(text) > 160 {
			text = append(text[:160], '…')
		}
		m.ui.printf("\n%d. %s:%d-%d\n   %s\n   %s\n", r.Rank, r.DocumentPath, r.StartLine, r.EndLine, r.HeadingPath, string(text))
	}
	return nil
}

func (m *menu) register() error {
	args := []string{"mcp", "add", "docgraph", "--", m.deps.Exe, "mcp", "--project", m.root}
	m.ui.printf("\nClaude Code (this project only):\n  claude %s\n", shellJoin(args))
	m.ui.printf("For every project, add --scope user. JSON config for other clients:\n")
	m.ui.printf("  {\"mcpServers\": {\"docgraph\": {\"type\": \"stdio\", \"command\": %q, \"args\": [\"mcp\", \"--project\", %q]}}}\n\n",
		m.deps.Exe, m.root)
	if _, err := exec.LookPath("claude"); err != nil {
		return nil
	}
	scope, err := m.ui.selectOne("Run the registration now?", "", []choice{
		{"Yes, for this project (local scope)", "local"},
		{"Yes, for all my projects (user scope)", "user"},
		{"No, I'll do it myself", "no"},
	})
	if err != nil || scope == "no" {
		return err
	}
	cmd := exec.Command("claude", append([]string{"mcp", "add", "--scope", scope}, args[2:]...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = m.root, m.ui.out, m.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude mcp add: %w", err)
	}
	m.ui.printf("✓ registered; restart Claude Code to pick it up.\n")
	return nil
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " '\"$\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
