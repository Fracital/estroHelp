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
		m.ResponseWriter.Write(modelField.ReplaceAll(m.buf.Bytes(), m.replacement))
		m.buf.Reset()
	}
	m.Flush()
}
