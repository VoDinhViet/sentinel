package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/restic"
)

// The restore is three screens one after the other, opened over the snapshot list: the folder to
// restore into (targetScreen), the job name typed to confirm (confirmScreen), and the outcome
// (resultScreen). The busy screen sits over the confirmation while restic works.

// targetScreen asks for the directory to restore into.
type targetScreen struct {
	job, repo string
	snap      restic.Snapshot
	target    string
	options   []string // the candidates the last tab listed
	problem   string
}

func (s targetScreen) onKey(m manageModel, key, text string) (manageModel, tea.Cmd) {
	if key != "tab" {
		s.options = nil
	}
	switch key {
	case "tab":
		completed, options := completePath(s.target)
		if completed != s.target || len(options) == 0 {
			s.target = completed
		}
		s.options = options
	case "esc":
		return m.pop(), nil
	case "enter":
		target := strings.TrimSpace(s.target)
		if !strings.HasPrefix(target, "/") {
			s.problem = "nhập đường dẫn tuyệt đối, ví dụ /tmp/restore"
			break
		}
		s.target = target
		return m.replaceTop(s).push(confirmScreen{job: s.job, repo: s.repo, snap: s.snap, target: target}), nil
	default:
		s.target, s.problem = editText(s.target, key, text), ""
	}
	return m.replaceTop(s), nil
}

func (s targetScreen) render(m manageModel) []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s của %s", shortID(s.snap.ID), s.job)),
		"",
		"Thư mục để restore vào (đường dẫn tuyệt đối):",
		"  " + s.target + lipglossAccent("▏"),
	}
	if len(s.options) > 0 {
		screenLines = append(screenLines, "  "+dimStyle.Render(truncate(strings.Join(s.options, "  "), max(m.width-4, 10))))
	}
	if s.problem != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(s.problem))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", "điền", "enter", "tiếp tục", "esc", "quay lại"))
}

// confirmScreen asks for the job name before the restore starts.
type confirmScreen struct {
	job, repo      string
	snap           restic.Snapshot
	target         string
	typed, problem string
}

func (s confirmScreen) onKey(m manageModel, key, text string) (manageModel, tea.Cmd) {
	switch key {
	case "esc":
		return m.pop(), nil
	case "enter":
		return s.start(m)
	}
	s.typed, s.problem = editText(s.typed, key, text), ""
	return m.replaceTop(s), nil
}

// start runs the restore in the background when the typed name matches the job.
func (s confirmScreen) start(m manageModel) (manageModel, tea.Cmd) {
	if s.typed != s.job {
		s.problem = "tên không khớp"
		return m.replaceTop(s), nil
	}
	ctx := m.startBusy(fmt.Sprintf("Đang restore %s của %s vào %s", shortID(s.snap.ID), s.job, s.target))
	actions := m.actions
	return m, tea.Batch(m.spin(), func() tea.Msg {
		hint, err := actions.restore(ctx, s.job, s.repo, s.snap, s.target)
		return restoreDoneMsg{hint: hint, err: err}
	})
}

func (s confirmScreen) render(m manageModel) []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s của %s", shortID(s.snap.ID), s.job)),
		"",
		wrap(fmt.Sprintf("Snapshot %s (%s) từ %s sẽ được restore vào %s.",
			shortID(s.snap.ID), s.snap.Time.Local().Format("2006-01-02 15:04"), s.repo, s.target), m.width-2),
	}
	if s.target == "/" {
		screenLines = append(screenLines, "", severityStyle(sevWarn).Render("Đích là / : file được restore tại chỗ và ghi đè dữ liệu hiện có."))
	}
	screenLines = append(screenLines, "", "Gõ tên job ("+s.job+") để xác nhận:", "  "+s.typed+lipglossAccent("▏"))
	if s.problem != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(s.problem))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "enter", "restore", "esc", "huỷ"))
}

// resultScreen tells how the restore ended. Any key closes it: back to the snapshot list after a
// restore, back to the tabs after a failure.
type resultScreen struct {
	text string
	bad  bool
}

// afterRestore replaces the target and confirmation screens with the outcome, over the snapshot list.
func (m manageModel) afterRestore(msg restoreDoneMsg) manageModel {
	m.endBusy()
	done, ok := m.top().(confirmScreen)
	if !ok {
		return m
	}
	m = m.pop().pop()
	if msg.err != nil {
		return m.push(resultScreen{text: "Restore thất bại: " + msg.err.Error(), bad: true})
	}
	text := fmt.Sprintf("Đã restore %s của %s từ %s vào %s.", shortID(done.snap.ID), done.job, done.repo, done.target)
	if msg.hint != "" {
		text += "\n\nNạp lại bằng lệnh:\n  " + msg.hint + "\n\nLệnh này thay thế dữ liệu hiện có: hãy thử trên database hoặc server tạm trước."
	}
	return m.push(resultScreen{text: text})
}

func (s resultScreen) onKey(m manageModel, _, _ string) (manageModel, tea.Cmd) {
	m = m.pop()
	if s.bad {
		m = m.pop()
	}
	return m, nil
}

func (s resultScreen) render(m manageModel) []string {
	style := severityStyle(sevOK)
	if s.bad {
		style = severityStyle(sevBad)
	}
	first, rest, _ := strings.Cut(s.text, "\n")
	screenLines := []string{style.Render(wrap(first, m.width-2))}
	if rest != "" {
		screenLines = append(screenLines, rest)
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "phím bất kỳ", "tiếp tục"))
}
