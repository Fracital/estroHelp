package api

import (
	"bytes"
	"net/http"
	"regexp"
)

var modelField = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)

// modelRewriter stamps the public Kali id over the upstream's model name in
// both SSE chunks and plain JSON bodies, so clients never see the base model.
// Output is held per line so a field split across two chunks is still caught.
type modelRewriter struct {
	http.ResponseWriter
	replacement []byte
	buf         bytes.Buffer
	// usage is filled from the upstream's reported token counts as lines pass through.
	usage tokenUsage
	// bytesOut counts the body for the fallback estimate when no usage is reported.
	bytesOut int
	// onLine sees every complete line (and the final unterminated remainder) before rewriting.
	onLine func([]byte)
}

func newModelRewriter(w http.ResponseWriter, publicModel string) *modelRewriter {
	return &modelRewriter{ResponseWriter: w, replacement: []byte(`"model":"` + publicModel + `"`)}
}

func (m *modelRewriter) Write(p []byte) (int, error) {
	m.buf.Write(p)
	for {
		i := bytes.IndexByte(m.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i+1)
		copy(line, m.buf.Next(i+1))
		m.usage.observe(line)
		if m.onLine != nil {
			m.onLine(line)
		}
		m.bytesOut += len(line)
		if _, err := m.ResponseWriter.Write(modelField.ReplaceAll(line, m.replacement)); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (m *modelRewriter) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close writes whatever did not end in a newline (a JSON body, typically).
func (m *modelRewriter) Close() {
	if m.buf.Len() > 0 {
		m.usage.observe(m.buf.Bytes())
		if m.onLine != nil {
			m.onLine(m.buf.Bytes())
		}
		m.bytesOut += m.buf.Len()
		m.ResponseWriter.Write(modelField.ReplaceAll(m.buf.Bytes(), m.replacement))
		m.buf.Reset()
	}
	m.Flush()
}
