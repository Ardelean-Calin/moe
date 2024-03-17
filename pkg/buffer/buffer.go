package buffer

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Ardelean-Calin/elmo/commands"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/nix"
	"github.com/smacker/go-tree-sitter/rust"
)

// Catppuccin Frappe
var theme = []string{
	"#303446", // base00
	"#292c3c", // base01
	"#414559", // base02
	"#51576d", // base03
	"#626880", // base04
	"#c6d0f5", // base05
	"#f2d5cf", // base06
	"#babbf1", // base07
	"#e78284", // base08
	"#ef9f76", // base09
	"#e5c890", // base0A
	"#a6d189", // base0B
	"#81c8be", // base0C
	"#8caaee", // base0D
	"#ca9ee6", // base0E
	"#eebebe", // base0F
}

// Contains the new cursor coordinates
type UpdateViewportMsg int
type LineChangedMsg int
type TreeReloadMsg struct {
	tree   *sitter.Node
	colors []byte
}

// SourceCode is the main container for the opened files. TODO name to something more generic, like Buffer?
type SourceCode struct {
	// Stores the raw data bytes. To be replaced with gapbuffer
	data []byte
	// Contains a base16 color for each character
	colors []byte
	// Cursor index
	cursor int
	// Treesitter representation
	tree *sitter.Node
	//
	lines map[int]LineInfo
}

// LineInfo describes a line. Using this I can easily index lines and get their length and indentation
type LineInfo struct {
	start       int
	end         int
	indentation int
}

// SetSource loads a file and computes the appropriate LineInfo's
func (s *SourceCode) SetSource(source []byte) {
	lines := make(map[int]LineInfo)
	i := 0
	prevLine := -1
	currentLine := 0
	foundChar := false
	lineInfo := LineInfo{
		start:       0,
		end:         0,
		indentation: 0,
	}
	for _, b := range source {
		if prevLine != currentLine {
			foundChar = false
			lineInfo.start = i
			lineInfo.end = i
			lineInfo.indentation = 0
			prevLine = currentLine
		}

		// Calculate the indentation by counting the number of space characters
		if b == '\t' && !foundChar {
			lineInfo.indentation += 4
		} else if b == ' ' && !foundChar {
			lineInfo.indentation++
		} else {
			foundChar = true
		}

		if b == '\n' {
			lineInfo.end = i
			lines[currentLine] = lineInfo
			currentLine++
		}
		i++
	}

	s.data = source
	s.colors = bytes.Repeat([]byte{0x05}, len(source))
	s.cursor = 0
	s.tree = nil
	s.lines = lines
}

// GetSlice returns the slice between start and end
func (s *SourceCode) GetSlice(start, end int) []byte {
	return s.data[start:end]
}

func (s *SourceCode) GetColors(start, end int) []byte {
	return s.colors[start:end]
}

// Returns a map of type lineIndex: {start in buffer, end in buffer}
func (s *SourceCode) Lines() map[int]LineInfo {
	return s.lines
}

type Viewport struct {
	offset        int
	width, height int
}

// Model represents an opened file.
type Model struct {
	Path     string   // Absolute path on disk.
	fd       *os.File // File descriptor.
	Focused  bool
	modified bool // Content was modified and not saved to disk
	// Used just once on load
	ready bool
	//  Then, the cursor will be strictly for display only (see footer.go)
	// TEMPORARY
	source   *SourceCode // This replaces everything below
	viewport Viewport    // Scrollable viewport
}

func New() Model {
	return Model{
		Path:     "",
		fd:       nil,
		Focused:  true,
		modified: false,
		ready:    false,
		source:   nil,
	}
}

func (m Model) Init() tea.Cmd {
	log.Printf("buffer.go: Init() called")
	return nil
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	// On Resize, re-render the viewport
	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = Viewport{offset: 0, height: msg.Height - 2, width: msg.Width}
			m.ready = true
			// Clear the default keybinds
		} else {
			m.viewport.height = msg.Height - 2
			m.viewport.width = msg.Width
		}

	case tea.KeyMsg:
		// Half page up
		if msg.String() == "ctrl+u" {
			m.viewport.offset = clamp(m.viewport.offset-m.viewport.height/2, 0, len(m.source.lines)-m.viewport.height+2)
		}
		// Half page down
		if msg.String() == "ctrl+d" {
			m.viewport.offset = clamp(m.viewport.offset+m.viewport.height/2, 0, len(m.source.lines)-m.viewport.height+2)
		}
		// TODO: Normal mode, insert mode, etc.
		if msg.String() == "j" {
			m.source.cursorDown(1)
		}
		if msg.String() == "k" {
			m.source.cursorUp(1)
		}
		if msg.String() == "l" {
			m.source.cursorRight(1)
		}
		if msg.String() == "h" {
			m.source.cursorLeft(1)
		}
		cmds = append(cmds, cmd)

	case tea.MouseMsg:
		switch msg.Button {
		// Scroll the viewport with the mouse wheel
		case tea.MouseButtonWheelUp:
			m.viewport.offset = clamp(m.viewport.offset-3, 0, len(m.source.lines)-m.viewport.height+2)
		case tea.MouseButtonWheelDown:
			m.viewport.offset = clamp(m.viewport.offset+3, 0, len(m.source.lines)-m.viewport.height+2)
		case tea.MouseButtonLeft:
			x, y := msg.X-6, msg.Y
			row := m.viewport.offset + y
			line := m.source.lines[row]
			var pos int
			if line.indentation > 0 {
				pos = min(x, line.indentation)/4 + clamp(x-line.indentation, 0, line.end)
			} else {
				pos = x
			}
			m.source.cursor = clamp(line.start+pos, line.start, line.end+1)
		}

	// A new syntax tree has been generated
	case TreeReloadMsg:
		m.source.colors = msg.colors
		m.source.tree = msg.tree
		// Issue a viewport update
		cmds = append(cmds, UpdateViewport, commands.ShowStatus("Treesitter loaded successfully"))
	}

	return m, tea.Batch(cmds...)
}

// View renders the Buffer content to screen
func (m Model) View() string {
	if m.source == nil {
		return lipgloss.NewStyle().
			Width(m.viewport.width).
			Height(m.viewport.height).
			Render("")
	}

	var sb strings.Builder
	start := m.viewport.offset
	end := m.viewport.offset + m.viewport.height
	for i := start; i < end; i++ {
		lineinfo := m.source.lines[i]
		line := m.source.GetSlice(lineinfo.start, lineinfo.end)
		colors := m.source.GetColors(lineinfo.start, lineinfo.end)

		// Write line numbers
		numberStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme[0x02]))
		sb.WriteString(numberStyle.Render(fmt.Sprintf("%4d  ", i+1)))

		for j, b := range line {
			if m.source.cursor == lineinfo.start+j {
				sb.WriteString(lipgloss.NewStyle().Reverse(true).Render(string(b)))
			} else {
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(theme[colors[j]])).Render(string(b)))
			}
		}
		// If the cursor is on a line end (aka \n), render a whitespace
		if m.source.cursor == lineinfo.end {
			sb.WriteString(lipgloss.NewStyle().Reverse(true).Render(" "))
		}

		// Last character in the viewport needs not be a newline, or
		// I will get a weird empty line at the end
		if i < end-1 {
			sb.WriteByte('\n')
		}

	}
	return sb.String()
}

//go:embed syntax/go/highlights.scm
var highlightsGo []byte

//go:embed syntax/rust/highlights.scm
var highlightsRust []byte

//go:embed syntax/nix/highlights.scm
var highlightsNix []byte

// GenerateSyntaxTree parses the source code using treesitter and generates
// a syntax tree for it.
func GenerateSyntaxTree(sourceCode *SourceCode, ext string) tea.Cmd {
	return func() tea.Msg {
		// Prepare the colors. One for each byte!
		colors := bytes.Repeat([]byte{0x05}, len(sourceCode.data))

		var lang *sitter.Language
		var highlights []byte
		if ext == ".go" {
			lang = golang.GetLanguage()
			highlights = highlightsGo
		} else if ext == ".rs" {
			lang = rust.GetLanguage()
			highlights = highlightsRust
		} else if ext == ".nix" {
			lang = nix.GetLanguage()
			highlights = highlightsNix
		} else {
			log.Panicf("Unsupported language: %s", ext)
		}
		tree, _ := sitter.ParseCtx(context.Background(), sourceCode.data, lang)

		q, err := sitter.NewQuery(highlights, lang)
		if err != nil {
			log.Panic(err)
		}
		qc := sitter.NewQueryCursor()
		qc.Exec(q, tree)

		// Iterate over query results
		for {
			m, ok := qc.NextMatch()
			if !ok {
				break
			}
			// log.Printf("M: %v", m)
			// Apply predicates filtering
			m = qc.FilterPredicates(m, sourceCode.data)
			for _, c := range m.Captures {
				name := q.CaptureNameForId(c.Index)
				// noRunes := utf8.RuneCount(sourceCode[b:e])

				// The most basic of syntax highlighting!
				var color uint8
				switch name {
				case "attribute":
					color = 0x0E
				case "comment":
					color = 0x03
				case "constant.builtin":
					color = 0x09
				case "escape":
					color = 0x0C
				case "function", "function.method", "function.macro":
					color = 0x0D
				case "keyword":
					color = 0x0E
				case "label":
					color = 0x0C
				case "number":
					color = 0x09
				case "operator":
					color = 0x0C
				case "package":
					color = 0x0D
				case "property":
					color = 0x0D
				case "punctuation.bracket":
					color = 0x05
				case "string":
					color = 0x0B
				case "type", "type.builtin":
					color = 0x0A
				case "variable.member":
					color = 0x0C
				case "variable.parameter":
					color = 0x08
				default:
					color = 0x05
				}

				for index := c.Node.StartByte(); index < c.Node.EndByte(); index++ {
					colors[index] = color
				}

			}
		}
		// Save the current tree and syntax highlighting
		return TreeReloadMsg{
			tree:   tree,
			colors: colors,
		}
	}
}

// OpenFile opens the given file inside the buffer
func (m *Model) OpenFile(path string) tea.Cmd {
	var err error
	// taken from helix
	fd, err := os.OpenFile(path, os.O_RDWR, 0664)
	if err != nil {
		return commands.ShowError(err)
	}

	content, err := io.ReadAll(fd)
	if err != nil {
		return commands.ShowError(err)
	}
	extension := filepath.Ext(path)

	source := SourceCode{}
	source.SetSource(content)

	m.source = &source
	m.viewport.offset = 0
	m.Path = path
	m.fd = fd
	m.modified = false

	return tea.Batch(
		UpdateViewport,
		GenerateSyntaxTree(&source, extension))

}

// Name returns the title of the buffer window to display
func (b Model) Name() string {
	_, name := path.Split(b.Path)
	return name
}

func (source *SourceCode) cursorDown(n int) {
}

func (source *SourceCode) cursorUp(n int) {
}

func (source *SourceCode) cursorLeft(n int) {
	source.cursor = clamp(source.cursor-n, 0, len(source.data))
}

func (source *SourceCode) cursorRight(n int) {
	source.cursor = clamp(source.cursor+n, 0, len(source.data))
}

func UpdateViewport() tea.Msg {
	return UpdateViewportMsg(0)
}

// clamp limits the value of val between [low, high)
func clamp(val, low, high int) int {
	return max(low, min(val, high-1))
}
