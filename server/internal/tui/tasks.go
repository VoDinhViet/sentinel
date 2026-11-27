package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/format"
)

// tasksShown is how many of a server's latest tasks the Việc part lists.
const tasksShown = 20

// confirmation is a task the person asked for and has not yet agreed to send.
type confirmation struct {
	action string
	params map[string]string
	text   string
}

// notice is a line under the header of a server: what happened to the last thing the person asked for.
type notice struct {
	text string
	bad  bool
}

// taskGivenMsg is the answer to a task the person asked for.
type taskGivenMsg struct {
	name   string // the server it was for
	action string
	task   ctlapi.Task
	err    error
}

// askTask gives one task to a server.
func (m model) askTask(name, action string, params map[string]string) tea.Cmd {
	source, ctx := m.source, m.ctx
	return func() tea.Msg {
		askCtx, cancel := context.WithTimeout(ctx, askTimeout)
		defer cancel()
		created, err := source.CreateTask(askCtx, name, action, params)
		return taskGivenMsg{name: name, action: action, task: created, err: err}
	}
}

// onBackupKey asks the person to confirm starting (b) or stopping (s) the backup of the job under the
// cursor. Nothing is sent before they say yes.
func (m model) onBackupKey(key string) (tea.Model, tea.Cmd) {
	report := m.open.detail.Report
	if m.open.current != backupsPart || report == nil || m.open.cursor >= len(report.Backups) {
		return m, nil
	}
	backup := report.Backups[m.open.cursor]
	action, params, text := agentapi.ActionBackupStart, map[string]string{"job": backup.Job, "repository": backup.Repository},
		fmt.Sprintf("Chạy backup job %s vào repository %s trên %s?", backup.Job, backup.Repository, m.open.name)
	if key == "s" {
		action, params, text = agentapi.ActionBackupStop, map[string]string{"job": backup.Job}, fmt.Sprintf("Dừng backup job %s trên %s?", backup.Job, m.open.name)
	}
	if !slices.Contains(report.Allow, action) { // the agent would refuse it: say why now
		m.open.notice = notice{text: fmt.Sprintf("%s chưa cho phép %s: bật control_plane.allow.backup trong config của nó rồi khởi động lại sentinel.", m.open.name, action), bad: true}
		return m, nil
	}
	m.open.notice = notice{}
	m.open.confirm = &confirmation{action: action, params: params, text: text}
	return m, nil
}

// onConfirmKey answers the question: y sends the task, any other key drops it.
func (m model) onConfirmKey(key string) (tea.Model, tea.Cmd) {
	asked := m.open.confirm
	m.open.confirm = nil
	if key != "y" {
		return m, nil
	}
	return m, m.askTask(m.open.name, asked.action, asked.params)
}

func (m model) afterTaskGiven(msg taskGivenMsg) (tea.Model, tea.Cmd) {
	if next, quit, over := m.fail(msg.err); over {
		return next, quit
	}
	if !m.opened || m.open.name != msg.name {
		return m, nil
	}
	if msg.err != nil {
		m.open.notice = notice{text: "Không giao được việc: " + msg.err.Error(), bad: true}
		return m, nil
	}
	m.open.notice = notice{text: fmt.Sprintf("Đã giao việc #%d (%s). Xem kết quả ở mục 5.", msg.task.ID, msg.action)}
	if m.open.loading {
		return m, nil
	}
	m.open.loading = true
	return m, m.askDetail(msg.name)
}

// tasksTable is the latest tasks of a server, newest first.
func tasksTable(tasks []ctlapi.Task, now time.Time, err error) table {
	t := table{title: "Việc", headers: []string{"#", "ACTION", "THAM SỐ", "TRẠNG THÁI", "LÚC"}, statusCol: 3, empty: "Chưa giao việc nào cho server này. Ở mục Backup: b chạy, s dừng."}
	if err != nil {
		t.empty = "Không đọc được danh sách việc: " + err.Error()
	}
	for _, task := range tasks {
		t.lines = append(t.lines, tableLine{
			cells:  []string{fmt.Sprint(task.ID), task.Action, paramsText(task.Params), task.State, format.Since(task.CreatedAt, now)},
			level:  taskLevel(task.State),
			detail: taskSentence(task),
		})
	}
	return t
}

func taskLevel(state string) level {
	switch state {
	case agentapi.TaskSucceeded:
		return levelOK
	case agentapi.TaskFailed:
		return levelBad
	case agentapi.TaskRefused:
		return levelWarn
	}
	return levelDim
}

// taskSentence is what the person wants to know of a task: how it ended and why.
func taskSentence(task ctlapi.Task) string {
	text := fmt.Sprintf("#%d %s %s: %s", task.ID, task.Action, paramsText(task.Params), task.State)
	switch {
	case task.Error != "":
		text += ": " + task.Error
	case task.Output != "":
		text += ": " + task.Output
	}
	return strings.TrimSpace(text)
}

// paramsText is "job=files repository=s3", in the same order every time.
func paramsText(params map[string]string) string {
	pairs := make([]string, 0, len(params))
	for key, value := range params {
		pairs = append(pairs, key+"="+value)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, " ")
}

// noticeLine is the line under the header of a server: the question when one is open, else the last notice.
func (s serverScreen) noticeLine(width int) string {
	switch {
	case s.confirm != nil:
		return " " + levelStyle(levelWarn).Render(truncate(s.confirm.text+"  y: đồng ý · phím khác: bỏ qua", width-2))
	case s.notice.text == "":
		return ""
	case s.notice.bad:
		return " " + levelStyle(levelBad).Render(truncate(s.notice.text, width-2))
	}
	return " " + levelStyle(levelOK).Render(truncate(s.notice.text, width-2))
}
