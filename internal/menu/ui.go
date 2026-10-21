// Package menu is the interactive `docgraph` menu (huh), modelled on
// mcplogview-go's configure wizard: a TUI in a terminal, plain line-based
// prompts on a pipe or with --plain / DOCGRAPH_PLAIN / TERM=dumb.
package menu

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
)

// errBack returns from a sub-flow to the main menu.
var errBack = errors.New("back")

// errInputClosed ends the menu when a piped input is exhausted.
var errInputClosed = errors.New("input ended before the menu finished")

type ui struct {
	in    *eofReader
	out   io.Writer
	plain bool
}

// eofReader hands out one line per Read and remembers that the input ran dry.
// huh's plain mode opens a fresh bufio.Scanner per prompt; on a pipe the first
// scanner would swallow every later answer, so lines are released one at a time.
type eofReader struct {
	br      *bufio.Reader
	pending []byte
	eof     bool
}

func newEOFReader(r io.Reader) *eofReader { return &eofReader{br: bufio.NewReader(r)} }

func (e *eofReader) Read(p []byte) (int, error) {
	if len(e.pending) == 0 {
		line, err := e.br.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				e.eof = true
			}
			return 0, err
		}
		e.pending = line
	}
	n := copy(p, e.pending)
	e.pending = e.pending[n:]
	return n, nil
}

type choice struct{ label, value string }

func (u *ui) run(fields ...huh.Field) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("prompt failed: %v", r)
		}
	}()
	f := huh.NewForm(huh.NewGroup(fields...)).WithAccessible(u.plain)
	if u.plain {
		// only plain prompts read through the line-wise reader; the TUI needs the real terminal
		f = f.WithOutput(u.out)
		if u.in != nil {
			f = f.WithInput(u.in)
		}
	}
	if err := f.Run(); err != nil {
		return err
	}
	if u.plain && u.in != nil && u.in.eof {
		return errInputClosed
	}
	return nil
}

func (u *ui) printf(format string, a ...any) { fmt.Fprintf(u.out, format, a...) }

func (u *ui) selectOne(title, desc string, opts []choice) (string, error) {
	var v string
	hopts := make([]huh.Option[string], len(opts))
	for i, o := range opts {
		hopts[i] = huh.NewOption(o.label, o.value)
	}
	sel := huh.NewSelect[string]().Title(title).Description(desc).Options(hopts...).Value(&v).Height(min(len(opts)+3, 18))
	if err := u.run(sel); err != nil {
		return "", err
	}
	return v, nil
}

func (u *ui) multiSelect(title, desc string, opts []choice) ([]string, error) {
	var v []string
	hopts := make([]huh.Option[string], len(opts))
	for i, o := range opts {
		hopts[i] = huh.NewOption(o.label, o.value)
	}
	ms := huh.NewMultiSelect[string]().Title(title).Description(desc).Options(hopts...).Value(&v).Height(min(len(opts)+3, 18))
	if err := u.run(ms); err != nil {
		return nil, err
	}
	return v, nil
}

func (u *ui) input(title, desc, def string, validate func(string) error) (string, error) {
	v := def
	in := huh.NewInput().Title(title).Description(desc).Value(&v)
	if validate != nil {
		// plain mode validates the raw answer before applying the default
		in = in.Validate(func(s string) error {
			if strings.TrimSpace(s) == "" && def != "" {
				s = def
			}
			return validate(s)
		})
	}
	if err := u.run(in); err != nil {
		return "", err
	}
	if strings.TrimSpace(v) == "" {
		v = def
	}
	return strings.TrimSpace(v), nil
}

func (u *ui) confirm(title string, def bool) (bool, error) {
	v := def
	if err := u.run(huh.NewConfirm().Title(title).Value(&v)); err != nil {
		return false, err
	}
	return v, nil
}

// StdinIsTerminal: a human has a terminal, an agent or a pipe does not.
var StdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
