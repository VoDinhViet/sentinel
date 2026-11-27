package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// keyHelp is one line of the key list.
type keyHelp struct{ key, does string }

// helpScreen lists the keys that work on the screen it was opened from; any key closes it.
type helpScreen struct {
	title string
	keys  []keyHelp
}

func (helpScreen) onKey(m manageModel, _, _ string) (manageModel, tea.Cmd) {
	return m.pop(), nil
}

func (s helpScreen) render(m manageModel) []string {
	screenLines := []string{titleStyle.Render(s.title), ""}
	for _, help := range s.keys {
		screenLines = append(screenLines, fmt.Sprintf("  %s  %s", keyStyle.Render(fmt.Sprintf("%-12s", help.key)), wrap(help.does, max(m.width-18, 20))))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "phím bất kỳ", "đóng"))
}

// tabHelp is the key list for the tab on show.
func (m manageModel) tabHelp() helpScreen {
	common := []keyHelp{
		{"tab  1-6", "chuyển giữa Backups, Uptime, Alerts, Doctor, Config và Server"},
		{"↑ ↓  j k", "chọn một dòng"},
		{"g  G", "dòng đầu và dòng cuối"},
		{"click  wheel", "bấm vào tab hoặc dòng; cuộn bằng wheel"},
		{"q  esc", "thoát"},
	}
	refresh := keyHelp{"r", "làm mới ngay (màn hình cũng tự làm mới mỗi 5 giây)"}
	var ownKeys []keyHelp
	switch m.tab {
	case backupsTab:
		ownKeys = []keyHelp{
			{"b", "backup job đang chọn ngay, chạy nền: đóng manage cũng không dừng"},
			{"x", "dừng backup đang chạy (bấm hai lần để xác nhận)"},
			{"s  enter", "liệt kê snapshot của job, rồi restore một cái"},
			{"a", "thêm job (files, postgres, mysql, mongodb) vào config"},
			{"e", "sửa job đang chọn"},
			{"d", "xoá job đang chọn khỏi config"},
			refresh,
		}
	case doctorTab:
		ownKeys = []keyHelp{{"r", "chạy lại các check"}}
	case serverTab:
		ownKeys = []keyHelp{{"r", "đọc lại tình trạng máy ngay (tab này tự đọc lại mỗi 5 giây khi đang mở)"}}
	case configTab:
		ownKeys = []keyHelp{
			{"a", "thêm một mục vào nhóm của dòng đang chọn"},
			{"e  enter", "sửa mục đang chọn"},
			{"d", "xoá mục đang chọn khỏi config"},
			{"t", "gửi tin thử tới notifier đang chọn (đọc config trong file, không cần khởi động lại)"},
			refresh,
		}
	default:
		ownKeys = []keyHelp{refresh}
	}
	return helpScreen{title: "Phím · " + m.tabs()[m.tab].title, keys: append(ownKeys, common...)}
}

// snapshotsHelp is the key list of the snapshots screen.
func snapshotsHelp() helpScreen {
	return helpScreen{title: "Snapshots", keys: []keyHelp{
		{"↑ ↓  j k", "chọn một snapshot"},
		{"enter  r", "restore snapshot đang chọn (hỏi thư mục đích, rồi tên job)"},
		{"click", "chọn một snapshot; bấm lần nữa để restore"},
		{"wheel", "cuộn"},
		{"esc  q", "quay lại danh sách job"},
	}}
}
