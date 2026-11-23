package main

import "fmt"

// keyHelp is one line of the key list.
type keyHelp struct{ key, does string }

// helpFor lists the keys that work on the screen the help was opened from.
func (m manageModel) helpFor() (title string, keys []keyHelp) {
	if m.helpReturn == modeSnapshots {
		return "Snapshots", []keyHelp{
			{"↑ ↓  j k", "chọn một snapshot"},
			{"enter  r", "restore snapshot đang chọn (hỏi thư mục đích, rồi tên job)"},
			{"click", "chọn một snapshot; bấm lần nữa để restore"},
			{"wheel", "cuộn"},
			{"esc  q", "quay lại danh sách job"},
		}
	}
	common := []keyHelp{
		{"tab  1-5", "chuyển giữa Backups, Uptime, Alerts, Doctor và Config"},
		{"↑ ↓  j k", "chọn một dòng"},
		{"g  G", "dòng đầu và dòng cuối"},
		{"r", "làm mới ngay (màn hình cũng tự làm mới mỗi 5 giây)"},
		{"click  wheel", "bấm vào tab hoặc dòng; cuộn bằng wheel"},
		{"q  esc", "thoát"},
	}
	if m.tab == doctorTab {
		return "Phím", append([]keyHelp{{"r", "chạy lại các check"}}, common[:1]...)
	}
	if m.tab != 0 {
		return "Phím", common
	}
	return "Phím", append([]keyHelp{
		{"b", "backup job đang chọn ngay (ctrl+c để huỷ)"},
		{"s  enter", "liệt kê snapshot của job, rồi restore một cái"},
		{"a", "thêm job (files, postgres, mysql, mongodb) vào config"},
		{"e", "sửa job đang chọn"},
		{"d", "xoá job đang chọn khỏi config"},
	}, common...)
}

func (m manageModel) renderHelp() []string {
	title, keys := m.helpFor()
	screenLines := []string{titleStyle.Render(title), ""}
	for _, help := range keys {
		screenLines = append(screenLines, fmt.Sprintf("  %s  %s", keyStyle.Render(fmt.Sprintf("%-12s", help.key)), wrap(help.does, max(m.width-18, 20))))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "phím bất kỳ", "đóng"))
}
