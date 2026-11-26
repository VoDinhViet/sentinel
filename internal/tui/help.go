package tui

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
	return "Phím", append(ownKeys, common...)
}

func (m manageModel) renderHelp() []string {
	title, keys := m.helpFor()
	if m.helpReturn == modeTable {
		title += " · " + m.tabs()[m.tab].title
	}
	screenLines := []string{titleStyle.Render(title), ""}
	for _, help := range keys {
		screenLines = append(screenLines, fmt.Sprintf("  %s  %s", keyStyle.Render(fmt.Sprintf("%-12s", help.key)), wrap(help.does, max(m.width-18, 20))))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "phím bất kỳ", "đóng"))
}
