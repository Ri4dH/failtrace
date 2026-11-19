package repro

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/Ri4dH/failtrace/internal/joblog"
)

// lineCollector keeps the last max lines written to it.
type lineCollector struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
}

func newLineCollector(max int) *lineCollector { return &lineCollector{max: max} }

func (c *lineCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.partial = append(c.partial, p...)
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			break
		}
		c.add(string(c.partial[:i]))
		c.partial = c.partial[i+1:]
	}
	return len(p), nil
}

func (c *lineCollector) add(line string) {
	c.lines = append(c.lines, joblog.Clean(line))
	if len(c.lines) > c.max {
		c.lines = c.lines[len(c.lines)-c.max:]
	}
}

// Lines returns the collected lines, including an unterminated last line.
func (c *lineCollector) Lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.partial) > 0 {
		c.add(string(c.partial))
		c.partial = nil
	}
	return append([]string{}, c.lines...)
}

// prefixWriter indents streamed output line by line.
type prefixWriter struct {
	mu      sync.Mutex
	w       io.Writer
	prefix  string
	midLine bool
}

func newPrefixWriter(w io.Writer, prefix string) *prefixWriter {
	return &prefixWriter{w: w, prefix: prefix}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out bytes.Buffer
	for _, c := range b {
		if !p.midLine {
			out.WriteString(p.prefix)
			p.midLine = true
		}
		out.WriteByte(c)
		if c == '\n' {
			p.midLine = false
		}
	}
	if _, err := p.w.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Flush ends a partially written line.
func (p *prefixWriter) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.midLine {
		_, _ = io.WriteString(p.w, "\n")
		p.midLine = false
	}
}

func tailLines(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

func tailText(s string, n int) string {
	return strings.Join(tailLines(strings.Split(strings.TrimRight(s, "\n"), "\n"), n), "\n")
}
