package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/colin-hofer/yapssh/internal/chat"
)

const (
	defaultWidth   = 110
	defaultHeight  = 32
	historyLimit   = 700
	pollInterval   = 600 * time.Millisecond
	typingInterval = 400 * time.Millisecond
)

type WindowSize struct {
	Width  int
	Height int
}

type Options struct {
	Input         io.Reader
	Output        io.Writer
	Width         int
	Height        int
	WindowChanges <-chan WindowSize
}

var (
	colorMuted   = lipgloss.Color("#6D7B86")
	colorAccent  = lipgloss.Color("#67D7AE")
	colorAccent2 = lipgloss.Color("#F1C76D")
	colorBorder  = lipgloss.Color("#23313B")
	colorFocus   = lipgloss.Color("#489B7A")
	colorStrong  = lipgloss.Color("#E6EEF2")
	colorDanger  = lipgloss.Color("#F07365")
	colorSelf    = lipgloss.Color("#94CBFF")
	colorPeer    = lipgloss.Color("#D7E0E4")
	colorSystem  = lipgloss.Color("#A8B4BB")
	colorSelect  = lipgloss.Color("#193247")
	colorCodeBg  = lipgloss.Color("#17232C")
	colorCodeFg  = lipgloss.Color("#D4E7F6")

	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(1, 2)
	focusedStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorFocus).Padding(1, 2)
	modalStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(1, 2)
	titleStyle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	accent2Style  = lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	statusStyle   = lipgloss.NewStyle().Foreground(colorAccent2)
	dangerStyle   = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(colorStrong).Background(colorSelect).Padding(0, 1)
)

type model struct {
	ctx    context.Context
	client *chat.Client

	width  int
	height int

	state  chat.Snapshot
	status string

	messages viewport.Model
	composer textarea.Model
	prompt   textinput.Model

	modal string

	hasNewBelow   bool
	newBelowCount int
	lastRenderIDs string

	keyDisambiguation bool
	typingFrame       int
	typingTicking     bool
}

type pollMsg struct{}
type typingTickMsg struct{}

type snapshotMsg struct {
	snapshot chat.Snapshot
	err      error
}

type actionMsg struct {
	status   string
	snapshot chat.Snapshot
	err      error
}

func Run(ctx context.Context, client *chat.Client, opts Options) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer client.Leave()
	client.StartHeartbeat(runCtx, 5*time.Second)

	m := newModel(runCtx, client)
	programOpts := []tea.ProgramOption{}
	if opts.Input != nil {
		programOpts = append(programOpts, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOpts = append(programOpts, tea.WithOutput(opts.Output))
	}
	if opts.Width > 0 && opts.Height > 0 {
		programOpts = append(programOpts, tea.WithWindowSize(opts.Width, opts.Height))
	}

	program := tea.NewProgram(m, programOpts...)
	if opts.WindowChanges != nil {
		go func() {
			for size := range opts.WindowChanges {
				program.Send(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
			}
		}()
	}
	go func() {
		<-runCtx.Done()
		program.Quit()
	}()

	_, err := program.Run()
	return err
}

func newModel(ctx context.Context, client *chat.Client) *model {
	composer := textarea.New()
	composer.Placeholder = "Type a message..."
	composer.CharLimit = 4000
	composer.SetHeight(3)
	composer.ShowLineNumbers = false
	km := composer.KeyMap
	km.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))
	composer.KeyMap = km
	styles := composer.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	composer.SetStyles(styles)
	composer.Focus()

	messages := viewport.New()
	messages.SoftWrap = true
	messages.MouseWheelEnabled = true
	messages.MouseWheelDelta = 2
	messages.LeftGutterFunc = func(ctx viewport.GutterContext) string { return " " }

	prompt := textinput.New()
	prompt.Placeholder = "Display name"

	return &model{
		ctx:      ctx,
		client:   client,
		status:   "connected",
		messages: messages,
		composer: composer,
		prompt:   prompt,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.snapshotCmd(), pollCmd())
}

func pollCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg {
		return pollMsg{}
	})
}

func typingTickCmd() tea.Cmd {
	return tea.Tick(typingInterval, func(time.Time) tea.Msg {
		return typingTickMsg{}
	})
}

func (m *model) snapshotCmd() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.client.Snapshot(historyLimit)
		return snapshotMsg{snapshot: snapshot, err: err}
	}
}

func (m *model) actionCmd(fn func() error, ok string) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return actionMsg{err: err}
		}
		snapshot, err := m.client.Snapshot(historyLimit)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: ok, snapshot: snapshot}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLayout()
		m.syncTranscript(false)
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		m.keyDisambiguation = msg.SupportsKeyDisambiguation()
		return m, nil
	case pollMsg:
		return m, tea.Batch(m.snapshotCmd(), pollCmd())
	case typingTickMsg:
		m.typingFrame = (m.typingFrame + 1) % 3
		if m.anyoneTyping() {
			return m, typingTickCmd()
		}
		m.typingTicking = false
		return m, nil
	case snapshotMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		previousIDs := messageIDs(m.state.Messages)
		m.state = msg.snapshot
		m.syncLayout()
		if previousIDs != messageIDs(m.state.Messages) {
			m.syncTranscript(false)
		}
		if m.anyoneTyping() && !m.typingTicking {
			m.typingTicking = true
			return m, typingTickCmd()
		}
		return m, nil
	case actionMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, m.snapshotCmd()
		}
		if msg.status != "" {
			m.status = msg.status
		}
		if msg.snapshot.Room != "" {
			previousIDs := messageIDs(m.state.Messages)
			m.state = msg.snapshot
			if previousIDs != messageIDs(m.state.Messages) {
				m.syncTranscript(true)
			}
		}
		return m, m.snapshotCmd()
	case tea.MouseWheelMsg:
		if m.modal == "" {
			var cmd tea.Cmd
			m.messages, cmd = m.messages.Update(msg)
			if m.messages.AtBottom() {
				m.hasNewBelow = false
				m.newBelowCount = 0
			}
			return m, cmd
		}
	case tea.KeyPressMsg:
		if m.modal != "" {
			return m.handleModalKey(msg)
		}
		return m.handleKey(msg)
	}
	if m.modal == "rename" {
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	before := m.composer.Value()
	m.composer, cmd = m.composer.Update(msg)
	if before != m.composer.Value() {
		_ = m.client.SetTyping(strings.TrimSpace(m.composer.Value()) != "")
	}
	return m, cmd
}

func (m *model) View() tea.View {
	width, height := m.viewportSize()
	base := lipgloss.NewStyle().Foreground(colorStrong).Width(width).Height(height)
	if width < 30 || height < 10 {
		view := tea.NewView(base.Render(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, mutedStyle.Render("Terminal too small.\nResize to at least 30x10."))))
		view.AltScreen = true
		view.MouseMode = tea.MouseModeCellMotion
		return view
	}

	content := m.render(width, height)
	if m.modal != "" {
		content = m.renderModal(content, width, height)
	}
	view := tea.NewView(base.Render(content))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+r":
		m.prompt.SetValue(m.state.Self.Name)
		m.prompt.Focus()
		m.modal = "rename"
		return m, nil
	case "pgup", "pageup":
		m.messages.PageUp()
		return m, nil
	case "pgdown", "pagedown":
		m.messages.PageDown()
		if m.messages.AtBottom() {
			m.hasNewBelow = false
			m.newBelowCount = 0
		}
		return m, nil
	case "home":
		m.messages.GotoTop()
		return m, nil
	case "end":
		m.messages.GotoBottom()
		m.hasNewBelow = false
		m.newBelowCount = 0
		return m, nil
	case "ctrl+u":
		m.messages.HalfPageUp()
		return m, nil
	case "ctrl+d":
		m.messages.HalfPageDown()
		if m.messages.AtBottom() {
			m.hasNewBelow = false
			m.newBelowCount = 0
		}
		return m, nil
	case "enter":
		body := strings.TrimSpace(m.composer.Value())
		if body == "" {
			return m, nil
		}
		m.composer.SetValue("")
		_ = m.client.SetTyping(false)
		return m, m.submitCmd(body)
	}

	var cmd tea.Cmd
	before := m.composer.Value()
	m.composer, cmd = m.composer.Update(msg)
	if before != m.composer.Value() {
		_ = m.client.SetTyping(strings.TrimSpace(m.composer.Value()) != "")
	}
	return m, cmd
}

func (m *model) handleModalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.modal {
	case "rename":
		switch msg.String() {
		case "esc":
			m.dismissModal()
			return m, nil
		case "enter":
			name := m.prompt.Value()
			m.dismissModal()
			return m, m.actionCmd(func() error {
				return m.client.Rename(name)
			}, "updated display name")
		}
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) submitCmd(body string) tea.Cmd {
	switch {
	case strings.HasPrefix(body, "/name "):
		name := strings.TrimSpace(strings.TrimPrefix(body, "/name "))
		return m.actionCmd(func() error {
			return m.client.Rename(name)
		}, "updated display name")
	case strings.HasPrefix(body, "/nick "):
		name := strings.TrimSpace(strings.TrimPrefix(body, "/nick "))
		return m.actionCmd(func() error {
			return m.client.Rename(name)
		}, "updated display name")
	case strings.HasPrefix(body, "/me "):
		action := strings.TrimSpace(strings.TrimPrefix(body, "/me "))
		return m.actionCmd(func() error {
			return m.client.SendAction(action)
		}, "sent")
	case body == "/help":
		return func() tea.Msg {
			return actionMsg{status: "commands: /name <name>, /nick <name>, /me <action>, ctrl+r rename, pgup/pgdn scroll"}
		}
	default:
		return m.actionCmd(func() error {
			return m.client.Send(body)
		}, "sent")
	}
}

func (m *model) dismissModal() {
	m.modal = ""
	m.prompt.Blur()
	m.composer.Focus()
}

func (m *model) viewportSize() (int, int) {
	width := m.width
	height := m.height
	if width == 0 {
		width = defaultWidth
	}
	if height == 0 {
		height = defaultHeight
	}
	return width, height
}

func (m *model) syncLayout() {
	width, height := m.viewportSize()
	mainWidth, _, contentWidth := chatLayoutWidths(width)
	bodyHeight := height - 4
	if bodyHeight < 8 {
		bodyHeight = 8
	}
	msgHeight := bodyHeight - 7
	if msgHeight < 3 {
		msgHeight = 3
	}
	if mainWidth < 1 {
		mainWidth = width
	}
	m.messages.SetWidth(contentWidth)
	m.messages.SetHeight(msgHeight)
	m.composer.SetWidth(contentWidth)
}

func (m *model) syncTranscript(forceBottom bool) {
	width := m.messages.Width()
	if width <= 0 {
		_, _, width = chatLayoutWidths(m.viewportWidth())
	}
	rendered := m.renderTranscript(width)
	wasAtBottom := forceBottom || m.messages.AtBottom()
	prevCount := m.messages.TotalLineCount()
	offset := m.messages.YOffset()
	m.messages.SetContentLines(rendered)
	if wasAtBottom {
		m.messages.GotoBottom()
		m.hasNewBelow = false
		m.newBelowCount = 0
		return
	}
	if len(rendered) > prevCount {
		m.hasNewBelow = true
		delta := (len(rendered) - prevCount + 2) / 3
		if delta < 1 {
			delta = 1
		}
		m.newBelowCount += delta
	}
	maxOffset := len(rendered) - m.messages.Height()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	m.messages.SetYOffset(offset)
}

func (m *model) viewportWidth() int {
	width, _ := m.viewportSize()
	return width
}

func (m *model) render(width, height int) string {
	m.syncLayout()
	header := m.renderHeader(width)
	bodyHeight := height - 4
	if bodyHeight < 8 {
		bodyHeight = 8
	}
	mainWidth, sidebarWidth, _ := chatLayoutWidths(width)

	newMsgLine := ""
	if m.hasNewBelow && !m.messages.AtBottom() {
		label := "down: new messages"
		if m.newBelowCount == 1 {
			label = "down: 1 new message"
		} else if m.newBelowCount > 1 {
			label = fmt.Sprintf("down: %d new messages", m.newBelowCount)
		}
		newMsgLine = accentStyle.Render(label) + "\n"
	}

	chatContent := m.messages.View() + "\n" + newMsgLine + m.renderComposerArea()
	main := focusedStyle.Width(mainWidth).Height(bodyHeight).Render(chatContent)

	body := main
	if sidebarWidth > 0 {
		sidebar := panelStyle.Width(sidebarWidth).Height(bodyHeight).Render(m.renderPresence())
		body = lipgloss.JoinHorizontal(lipgloss.Top, main, sidebar)
	}

	footerText := "enter send"
	if m.keyDisambiguation {
		footerText += " · shift+enter newline"
	}
	footerText += " · ctrl+r rename · /name <name> · /me <action> · pgup/pgdn scroll · ctrl+c quit"
	footer := lipgloss.NewStyle().Padding(0, 2).Width(width).Render(mutedStyle.Render(footerText))
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *model) renderHeader(width int) string {
	room := m.state.Room
	if room == "" {
		room = chat.DefaultRoomName
	}
	selfName := m.state.Self.Name
	if selfName == "" {
		selfName = m.client.Identity().Name
	}
	online := len(m.state.Presence)
	people := "people"
	if online == 1 {
		people = "person"
	}
	status := m.status
	if status == "" {
		status = "connected"
	}
	return lipgloss.NewStyle().Padding(1, 2, 0, 2).Width(width).Render(
		titleStyle.Render("yapssh") +
			mutedStyle.Render(" · ") +
			accent2Style.Render(room) +
			mutedStyle.Render(fmt.Sprintf(" · %d %s online", online, people)) +
			mutedStyle.Render(" · ") +
			lipgloss.NewStyle().Foreground(colorStrong).Bold(true).Render(selfName) +
			"  " + statusStyle.Render(status),
	)
}

func (m *model) renderPresence() string {
	lines := []string{titleStyle.Render("People")}
	if len(m.state.Presence) == 0 {
		lines = append(lines, "", mutedStyle.Render("Waiting for connections..."))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "")
	for _, p := range m.state.Presence {
		dot := lipgloss.NewStyle().Foreground(colorAccent).Render("●")
		nameStyle := lipgloss.NewStyle().Foreground(colorPeer)
		if p.Self {
			nameStyle = lipgloss.NewStyle().Foreground(colorSelf).Bold(true)
		}
		name := truncate(p.Name, 18)
		line := dot + " " + nameStyle.Render(name)
		if p.Self {
			line += mutedStyle.Render(" you")
		}
		if p.Sessions > 1 {
			line += mutedStyle.Render(fmt.Sprintf(" x%d", p.Sessions))
		}
		if p.Typing && !p.Self {
			line += accentStyle.Render(" " + m.typingDots())
		}
		lines = append(lines, line)
		lines = append(lines, mutedStyle.Render("  seen "+relativeTime(p.LastSeen)))
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderComposerArea() string {
	typing := m.typingSummary()
	if typing != "" {
		typing = accentStyle.Render(typing)
	}
	return typing + "\n" + m.composer.View()
}

func (m *model) renderModal(content string, width, height int) string {
	boxWidth := width / 2
	if boxWidth < 42 {
		boxWidth = width - 8
	}
	if boxWidth < 10 {
		boxWidth = width - 2
	}
	box := modalStyle.Width(boxWidth).Render(m.modalBody())
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#071018")).Background(lipgloss.Color("#071018"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceStyle(dim),
	)
}

func (m *model) modalBody() string {
	switch m.modal {
	case "rename":
		return strings.Join([]string{
			titleStyle.Render("Rename"),
			"",
			"Choose the name other people see in this room.",
			"",
			m.prompt.View(),
			"",
			mutedStyle.Render("enter confirm · esc cancel"),
		}, "\n")
	default:
		return ""
	}
}

func (m *model) renderTranscript(width int) []string {
	if width <= 0 {
		width = 80
	}
	if len(m.state.Messages) == 0 {
		return []string{
			"",
			accentStyle.Render("Welcome to " + firstNonEmpty(m.state.Room, chat.DefaultRoomName)),
			"",
			mutedStyle.Render("This is the beginning of the conversation."),
			mutedStyle.Render("Press ctrl+r to rename yourself, or start typing."),
		}
	}
	lines := make([]string, 0, len(m.state.Messages)*3)
	prevDay := time.Time{}
	selfHandle := mentionToken(m.state.Self.Name)
	for i, msg := range m.state.Messages {
		if !msg.SentAt.IsZero() && !prevDay.IsZero() && !sameDay(prevDay, msg.SentAt) {
			lines = append(lines, "", mutedStyle.Render("-- "+msg.SentAt.Format("Mon Jan 2")+" --"), "")
		}
		if i > 0 && msg.Kind == chat.KindChat {
			lines = append(lines, "")
		}
		lines = append(lines, m.renderMessage(msg, selfHandle, width)...)
		if !msg.SentAt.IsZero() {
			prevDay = msg.SentAt
		}
	}
	return lines
}

func (m *model) renderMessage(msg chat.Message, selfHandle string, width int) []string {
	local := msg.UserID != "" && msg.UserID == m.state.Self.UserID
	switch msg.Kind {
	case chat.KindJoin:
		return []string{mutedStyle.Render(relativeTime(msg.SentAt)+" · ") + lipgloss.NewStyle().Foreground(colorAccent).Render(msg.Name+" joined")}
	case chat.KindLeave:
		return []string{mutedStyle.Render(relativeTime(msg.SentAt)+" · ") + mutedStyle.Render(msg.Name+" left")}
	case chat.KindRename:
		return []string{renderRename(msg)}
	case chat.KindAction:
		body := fmt.Sprintf("* %s %s", msg.Name, msg.Body)
		return wrapStyled(body, width, lipgloss.NewStyle().Foreground(colorAccent2))
	}

	nameStyle := lipgloss.NewStyle().Foreground(colorPeer).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorPeer)
	if local {
		nameStyle = lipgloss.NewStyle().Foreground(colorSelf).Bold(true)
		bodyStyle = lipgloss.NewStyle().Foreground(colorSelf)
	}
	header := mutedStyle.Render(relativeTime(msg.SentAt)) + mutedStyle.Render(" · ") + nameStyle.Render(msg.Name)
	if !local && selfHandle != "" && mentionsHandle(msg.Body, selfHandle) {
		header += accent2Style.Render(" @you")
	}
	out := []string{header}
	for i, line := range wrapText(msg.Body, width-3) {
		barColor := colorAccent
		if local {
			barColor = colorSelf
		}
		bar := lipgloss.NewStyle().Foreground(barColor).Render("▎")
		out = append(out, bar+" "+renderInline(line, bodyStyle, selfHandle))
		if i > 80 {
			break
		}
	}
	return out
}

func renderRename(msg chat.Message) string {
	oldName := strings.TrimSpace(msg.Body)
	newName := strings.TrimSpace(msg.Name)
	prefix := mutedStyle.Render(relativeTime(msg.SentAt) + " · ")
	if oldName != "" && newName != "" && !strings.EqualFold(oldName, newName) {
		return prefix + lipgloss.NewStyle().Foreground(colorAccent2).Render(oldName+" is now ") + accent2Style.Render(newName)
	}
	if newName != "" {
		return prefix + lipgloss.NewStyle().Foreground(colorAccent2).Render(newName+" updated their name")
	}
	return prefix + lipgloss.NewStyle().Foreground(colorAccent2).Render("someone updated their name")
}

func wrapStyled(body string, width int, style lipgloss.Style) []string {
	lines := wrapText(body, width)
	for i := range lines {
		lines[i] = style.Render(lines[i])
	}
	return lines
}

func renderInline(line string, base lipgloss.Style, selfHandle string) string {
	var out strings.Builder
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		value := token.String()
		switch {
		case strings.HasPrefix(value, "@") && selfHandle != "" && strings.EqualFold(cleanMention(value), selfHandle):
			out.WriteString(accent2Style.Render(value))
		case strings.HasPrefix(value, "@"):
			out.WriteString(accentStyle.Render(value))
		case strings.HasPrefix(value, "`") && strings.HasSuffix(value, "`") && len(value) > 1:
			out.WriteString(lipgloss.NewStyle().Foreground(colorCodeFg).Background(colorCodeBg).Render(strings.Trim(value, "`")))
		default:
			out.WriteString(base.Render(value))
		}
		token.Reset()
	}
	for _, r := range line {
		if unicode.IsSpace(r) {
			flush()
			out.WriteString(base.Render(string(r)))
			continue
		}
		token.WriteRune(r)
	}
	flush()
	return out.String()
}

func wrapText(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var lines []string
	for _, rawLine := range strings.Split(text, "\n") {
		words := strings.Fields(rawLine)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		var current string
		for _, word := range words {
			if len([]rune(word)) > width {
				if current != "" {
					lines = append(lines, current)
					current = ""
				}
				lines = append(lines, splitLongWord(word, width)...)
				continue
			}
			next := word
			if current != "" {
				next = current + " " + word
			}
			if len([]rune(next)) > width {
				lines = append(lines, current)
				current = word
			} else {
				current = next
			}
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	return lines
}

func splitLongWord(word string, width int) []string {
	runes := []rune(word)
	var lines []string
	for len(runes) > width {
		lines = append(lines, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		lines = append(lines, string(runes))
	}
	return lines
}

func (m *model) typingSummary() string {
	var names []string
	for _, presence := range m.state.Presence {
		if presence.Self || !presence.Typing {
			continue
		}
		names = append(names, presence.Name)
	}
	dots := m.typingDots()
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " is typing " + dots
	case 2:
		return names[0] + " and " + names[1] + " are typing " + dots
	default:
		return fmt.Sprintf("%s and %d others are typing %s", names[0], len(names)-1, dots)
	}
}

func (m *model) anyoneTyping() bool {
	for _, presence := range m.state.Presence {
		if !presence.Self && presence.Typing {
			return true
		}
	}
	return false
}

func (m *model) typingDots() string {
	frames := []string{".  ", ".. ", "..."}
	return frames[m.typingFrame%len(frames)]
}

func chatLayoutWidths(totalWidth int) (mainWidth, sidebarWidth, contentWidth int) {
	if totalWidth <= 0 {
		totalWidth = defaultWidth
	}
	if totalWidth >= 88 {
		sidebarWidth = 28
	}
	mainWidth = totalWidth - sidebarWidth
	if mainWidth < 30 {
		mainWidth = totalWidth
		sidebarWidth = 0
	}
	contentWidth = mainWidth - 6
	if contentWidth < 12 {
		contentWidth = 12
	}
	return mainWidth, sidebarWidth, contentWidth
}

func messageIDs(messages []chat.Message) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(message.ID)
		b.WriteByte('|')
	}
	return b.String()
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return "now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm", int(diff.Minutes()))
	case sameDay(t, time.Now()):
		return t.Format("15:04")
	default:
		return t.Format("Jan 2 15:04")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func mentionToken(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	dashed := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dashed = false
		case r == '-' || r == '_':
			if !dashed && b.Len() > 0 {
				b.WriteRune(r)
				dashed = true
			}
		default:
			if !dashed && b.Len() > 0 {
				b.WriteByte('-')
				dashed = true
			}
		}
	}
	return strings.Trim(b.String(), "-_")
}

func mentionsHandle(body, handle string) bool {
	for _, token := range strings.Fields(body) {
		if strings.HasPrefix(token, "@") && strings.EqualFold(cleanMention(token), handle) {
			return true
		}
	}
	return false
}

func cleanMention(value string) string {
	return strings.Trim(strings.TrimPrefix(value, "@"), ".,:;!?()[]{}<>\"'")
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max < 2 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
