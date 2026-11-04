package api

import (
	"bytes"
	"net/http"
	"regexp"

	"elichat/internal/relay"
)

var modelField = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)

// modelRewriter stamps the public Kali id over the upstream's model name in
// both SSE chunks and plain JSON bodies, so clients never see the base model.
// Output is held per line so a field split across two chunks is still caught.
// Mid-stream error events are rewritten too: a context overflow becomes the
// event the dialect's clients recover from, and vendor wording is scrubbed.
type modelRewriter struct {
	http.ResponseWriter
	replacement []byte
	buf         bytes.Buffer
	shape       relay.Shape
	responses   bool
	// pendingEvent holds an SSE `event:` line until its data line is seen, so a
	// rewritten error can replace the event name as well.
	pendingEvent []byte
	// usage is filled from the upstream's reported token counts as lines pass through.
	usage tokenUsage
	// bytesOut counts the body for the fallback estimate when no usage is reported.
	bytesOut int
	// onLine sees every complete line (and the final unterminated remainder) before rewriting.
	onLine func([]byte)
}

func newModelRewriter(w http.ResponseWriter, publicModel string, shape relay.Shape, responses bool) *modelRewriter {
	return &modelRewriter{ResponseWriter: w, replacement: []byte(`"model":"` + publicModel + `"`), shape: shape, responses: responses}
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
		if err := m.emit(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (m *modelRewriter) emit(line []byte) error {
	m.usage.observe(line)
	if m.onLine != nil {
		m.onLine(line)
	}
	m.bytesOut += len(line)
	if bytes.HasPrefix(bytes.TrimLeft(line, " "), []byte("event:")) {
		m.flushPending()
		m.pendingEvent = line
		return nil
	}
	if out, ok := relay.RewriteErrorLine(m.shape, m.responses, line); ok {
		// The replacement names its own event when the dialect needs one.
		if bytes.HasPrefix(out, []byte("event:")) {
			m.pendingEvent = nil
		} else {
			m.flushPending()
		}
		_, err := m.ResponseWriter.Write(append(out, '\n'))
		return err
	}
	m.flushPending()
	_, err := m.ResponseWriter.Write(modelField.ReplaceAll(line, m.replacement))
	return err
}

func (m *modelRewriter) flushPending() {
	if m.pendingEvent != nil {
		m.ResponseWriter.Write(m.pendingEvent)
		m.pendingEvent = nil
	}
}

func (m *modelRewriter) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close writes whatever did not end in a newline (a JSON body, typically).
func (m *modelRewriter) Close() {
	if m.buf.Len() > 0 {
		rest := make([]byte, m.buf.Len())
		copy(rest, m.buf.Bytes())
		m.buf.Reset()
		m.emit(rest)
	}
	m.flushPending()
	m.Flush()
}
