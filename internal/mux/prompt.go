package mux

import "panewright/internal/vt"

// prompt is a one-line text entry shown over the status bar (e.g. for
// rename-window). It captures keyboard input until committed or cancelled.
type prompt struct {
	label  string
	buf    []rune
	onDone func(string)
}

// startPromptLocked opens a status-line prompt seeded with initial text.
func (m *Manager) startPromptLocked(label, initial string, onDone func(string)) {
	m.prompt = &prompt{label: label, buf: []rune(initial), onDone: onDone}
	m.comp.invalidate()
	m.dirty = true
}

// PromptActive reports whether a status-line prompt is capturing input.
func (m *Manager) PromptActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prompt != nil
}

// PromptRune appends a typed character to the active prompt.
func (m *Manager) PromptRune(r rune) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prompt != nil {
		m.prompt.buf = append(m.prompt.buf, r)
		m.dirty = true
	}
}

// PromptBackspace deletes the last character of the active prompt.
func (m *Manager) PromptBackspace() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prompt != nil && len(m.prompt.buf) > 0 {
		m.prompt.buf = m.prompt.buf[:len(m.prompt.buf)-1]
		m.dirty = true
	}
}

// PromptCommit runs the prompt callback with the entered text and closes it.
func (m *Manager) PromptCommit() {
	m.mu.Lock()
	p := m.prompt
	m.prompt = nil
	m.comp.invalidate()
	m.dirty = true
	m.mu.Unlock()
	if p != nil && p.onDone != nil {
		p.onDone(string(p.buf))
	}
}

// PromptCancel closes the prompt without running its callback.
func (m *Manager) PromptCancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompt = nil
	m.comp.invalidate()
	m.dirty = true
}

// drawPrompt renders the prompt over the bottom row and parks the cursor at the
// end of the entered text.
func (m *Manager) drawPrompt(f *Frame) {
	row := m.statusRow()
	att := m.msgAtt
	for x := 0; x < m.cols; x++ {
		f.set(x, row, vt.Cell{R: ' ', A: att})
	}
	text := m.prompt.label + string(m.prompt.buf)
	putString(f, 0, row, text, att)
	cx := len([]rune(text))
	if cx >= m.cols {
		cx = m.cols - 1
	}
	f.curX, f.curY, f.curVis = cx, row, true
}
