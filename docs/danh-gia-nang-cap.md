# Đánh giá nâng cấp Sentinel

Ngày đánh giá: 2026-11-26. Phạm vi: toàn bộ Sentinel (backup, giám sát và cảnh báo, deploy và release). Giữ nguyên định hướng hiện tại: chỉ CLI và daemon, không AI, không web UI, không central server.

## Tóm tắt

Phần lõi đã chắc: restic chạy bằng danh sách đối số, secret không nằm trong argv, mọi thao tác đều có timeout, lock theo job, restore target được hardening, alert lưu trong SQLite, `make test` gồm lint, gosec, govulncheck, vet và test.

Rủi ro lớn nhất là ba chỗ có thể **mất backup hoặc mất cảnh báo mà không ai biết**:

1. Daemon chết thì không có gì báo.
2. Gửi alert thất bại thì bỏ luôn, kể cả tin RESOLVED.
3. `restic check` mặc định tắt, và khi bật cũng chỉ kiểm tra cấu trúc.

Ba vấn đề phụ khi nâng cấp và cấu hình: `install.sh` không restart service và không rollback được; binary cũ mở DB mới hơn mà không báo gì; validate cấu hình còn hở.

## Điểm mạnh hiện có

- `restic` nhận repository và password qua biến môi trường, biến mà config tham chiếu không bị truyền cho restic (`internal/restic/client.go:107`).
- Mỗi job có `flock` riêng, slot `max_parallel` mặc định 1 (`internal/backup/lock.go`), một daemon cho mỗi state dir (`internal/daemon/daemon.go:62`).
- Restore target phải thuộc về user, mode 0700, không symlink, thư mục hệ thống cần `--force` (`internal/backup/restoretarget.go`).
- Alert có vòng đời mở, nhắc lại, đóng; trạng thái lưu trong SQLite nên restart không báo lặp (`internal/alert/alert.go`).
- Notifier gửi song song, thử lại 3 lần, không follow redirect, SMTP bắt buộc TLS (`internal/notify/notify.go`).
- Migration SQLite có version bằng `PRAGMA user_version` (`internal/store/store.go:149`).
- Unit systemd đã hardening, `TimeoutStopSec=2h30m` khớp `shutdown_wait`.

## Phát hiện

### Backup

| # | Phát hiện | Bằng chứng |
|---|---|---|
| B1 | `restic check` mặc định tắt vì `verify_schedule` không có giá trị mặc định; chỉ kiểm tra cấu trúc, không đọc dữ liệu | `internal/config/defaults.go`, `internal/restic/client.go:436` |
| B2 | Kết quả `restic check` không được lưu, `status` không hiển thị; không có lệnh chạy tay | `internal/daemon/daemon.go:210` |
| B3 | Không có bài kiểm tra restore định kỳ | không có code tương ứng |
| B4 | `forget --prune` sau mỗi lần backup; prune cần khóa độc quyền nên thất bại khi đang có check hoặc backup khác, và không được thử lại | `internal/restic/client.go:376`, `internal/backup/runner.go:190` |
| B5 | Không có `restic copy` sang repository thứ hai; mỗi repository dump database lại từ đầu | `internal/backup/runner.go:93` |
| B6 | Một timeout chung cho mọi repository của job, repository đầu chậm sẽ ăn thời gian của repository sau | `internal/backup/runner.go:89` |
| B7 | `sentinel backup run` chạy từ CLI không thử lại khóa và không gửi alert | `cmd/sentinel/backup.go:107` |
| B8 | Snapshot chưa hoàn chỉnh (restic exit 3) bị tính là lỗi, bỏ qua retention | `internal/restic/client.go:358` |
| B9 | Không có lệnh `unlock` cho lock cũ | tìm `unlock` không thấy |
| B10 | Restore và check không dùng `nice`/`ionice`; không có `--limit-download` | `internal/backup/restore.go:18` |
| B11 | Thiếu: `pg_dumpall` (roles, globals), chế độ mọi database, SQLite, Redis, `keep-within`, `keep-hourly`, `exclude-caches` | tìm không thấy |
| B12 | Không có version cho schema config; chỉ có bảng `renamedKeys` | `internal/config/load.go:187` |
| B13 | Validate không chặn `timeout` âm và retention âm; nếu mọi số retention đều âm thì `forget --prune` chạy mà không có luật giữ | `internal/config/validate.go`, `internal/restic/client.go:384` |
| B14 | Probe kết nối database truyền toàn bộ `os.Environ()` cho tiến trình dump, khác với đường backup | `internal/engine/probe.go:57` |
| B15 | Mật khẩu Postgres đi qua `PGPASSWORD`; mật khẩu repository không có tùy chọn file | `internal/engine/postgres.go:39`, `internal/config/validate.go:21` |
| B16 | Trạng thái "Stalled" (10 phút không có dữ liệu) chỉ hiển thị, không tạo alert | `internal/backup/progress.go:55` |

### Giám sát và cảnh báo

| # | Phát hiện | Bằng chứng |
|---|---|---|
| M1 | Không ai biết khi daemon chết: không heartbeat, không `sd_notify`, không `WatchdogSec`; `status` và `doctor` không nói daemon có đang chạy không | `deploy/sentinel.service` |
| M2 | Lỗi gửi alert bị bỏ qua và `LastNotified` đã được đặt trước khi gửi, nên alert mở gửi lỗi sẽ im lặng tới lần nhắc kế tiếp (tối đa 1 giờ); tin RESOLVED lỗi thì mất hẳn | `internal/alert/alert.go:87`, `:97` |
| M3 | Gửi alert nằm trong task probe, thử lại có thể mất khoảng 50 giây, trong thời gian đó cron bỏ lượt probe | `internal/daemon/daemon.go:78` |
| M4 | Một lần thành công là đóng alert uptime, không chống flapping | `internal/daemon/uptime_tracker.go:74` |
| M5 | Không có cảnh báo ổ đĩa đầy. Phần đo CPU/RAM/disk đã bị xóa cùng đợt bỏ Beszel (commit `dda2b02`) | `internal/store/store.go:65` |
| M6 | Không reload cấu hình khi đổi file, phải restart | tìm SIGHUP không thấy |
| M7 | Alert của check hoặc job đã xóa khỏi config vẫn mở mãi | `internal/store/store.go:389` |
| M8 | Probe còn ít: chỉ http (GET), tcp, ping; không có DNS, header, auth, ngưỡng latency | `internal/uptime/uptime.go:32` |
| M9 | Notifier: chỉ Telegram, SMTP, Discord, Slack; không có webhook chung, ntfy | `internal/notify/notify.go:68` |
| M10 | `status` luôn thoát mã 0 dù còn alert mở | `cmd/sentinel/status.go:37` |
| M11 | Không báo khi daemon start hoặc stop; log chỉ có dạng text | `internal/daemon/daemon.go:110` |
| M12 | Không có xuất metrics, không có quiet hours hay mute | tìm không thấy |

### Deploy và release

| # | Phát hiện | Bằng chứng |
|---|---|---|
| D1 | Không có CI: lint, test, govulncheck, e2e chỉ chạy khi có người chạy tay | không có `.github/` |
| D2 | Không ký, không SBOM; `checksums.txt` lấy cùng nơi với archive nên chỉ chống hỏng file | `deploy/install.sh:176`, `.goreleaser.yaml` |
| D3 | Không có `sentinel upgrade`; nâng cấp là kéo repo, build, chạy lại `install.sh` | `README.md:31` |
| D4 | `install.sh` ghi đè binary, không giữ bản cũ, không restart service | `deploy/install.sh:232`, `:244` |
| D5 | Binary cũ mở DB mới hơn thì vòng migration không làm gì, không báo lỗi | `internal/store/store.go:154` |
| D6 | restic, rclone đã cài thì giữ nguyên dù cũ; Docker lấy bản Alpine, nên phiên bản lệch nhau giữa các máy | `deploy/install.sh:186`, `Dockerfile:5` |
| D7 | Image `golang:1.27` và `chrislusf/seaweedfs:latest` không ghim phiên bản | `Makefile:12`, `test/docker-compose.yml:18` |
| D8 | `install.sh` không được test hay shellcheck | `.claude/rules/deploy-ops.md:12` |
| D9 | Hai đường release (`make dist` và goreleaser), chưa nói đường nào chính thức; chưa có CHANGELOG | `Makefile:31`, `.goreleaser.yaml` |
| D10 | Repo private nên tải release không đăng nhập không dùng được; Docker không có image đã publish | `README.md:15`, `deploy/docker-compose.yml:3` |
| D11 | `make test` đỏ ở bước govulncheck: toolchain Go 1.27.1 có nhiều lỗ hổng thư viện chuẩn (net/http, crypto/tls, crypto/x509, net/textproto; bản vá từ go1.27.2 tới go1.27.4) và `golang.org/x/net` v0.58.0 cần v0.60.0. Binary build bằng toolchain này mang các lỗi đó, và Sentinel có gọi tới các đường bị ảnh hưởng (gửi alert, heartbeat, probe HTTP) | `go.mod:3`, chạy `make vuln`. **Đã xử lý**: go 1.27.4 và x/net v0.60.0, `make test` xanh |

## Lộ trình

### P1 (đã làm cùng đợt đánh giá này)

1. Gửi lại alert bị lỗi, kể cả RESOLVED (M2).
2. Heartbeat gọi URL ngoài và `status` hiện daemon có đang chạy (M1).
3. Bật `restic check` hàng tuần mặc định (B1).
4. Cảnh báo ổ đĩa sắp đầy, chỉ disk (M5).
5. Từ chối mở DB có schema mới hơn bản đang chạy (D5).
6. `install.sh` giữ bản cũ và restart service khi nâng cấp (D4).
7. Chặn `timeout` và retention âm (B13).
8. Probe kết nối database không truyền secret không liên quan (B14).

Rà soát an toàn sau khi làm P1 đã tìm ra và sửa thêm:

- `restic check` hằng tuần và `forget --prune` của job có thể va khóa nhau: cả hai nay chờ khóa tối đa 30 phút (`--retry-lock`).
- Tin alert gửi lại không bị gửi trùng khi một lần gửi còn đang chạy, và thứ tự lưu vào SQLite theo đúng thứ tự thay đổi.
- Alert đĩa của đường dẫn đã bị bỏ khỏi config được đóng lại thay vì mở mãi.
- Hai tiến trình mở DB cũ cùng lúc (daemon vừa restart và `sentinel status`) không còn làm migration chạy hai lần.

### P2

- CI trên GitHub Actions chạy `make test`, thêm e2e khi có docker (D1).
- Một đường release duy nhất (goreleaser) có checksum ký, SBOM và CHANGELOG (D2, D9).
- Tách prune khỏi mỗi lần backup: `prune_schedule` và `--max-unused` (B4).
- Lưu kết quả `restic check` và hiện trong `status`; thêm `--read-data-subset` và lệnh chạy tay (B1, B2).
- Kiểm tra restore định kỳ cho job database (B3).
- `sentinel backup run` cũng gửi alert (B7).
- Alert khi backup bị stalled (B16).
- Timeout riêng cho từng repository (B6).
- Gửi alert ngoài task probe (M3), `recover_after` chống flapping (M4).
- Đóng alert của check hoặc job đã xóa (M7).
- Notifier webhook chung hoặc ntfy (M9).
- Ghim phiên bản restic trong Dockerfile và nâng restic cũ khi chạy lại installer (D6, D7).
- Chạy shellcheck cho `install.sh` (D8).
- `pg_dumpall --globals-only` (B11), `restic copy` sang repository thứ hai (B5).

### P3

- Reload cấu hình bằng SIGHUP (M6).
- Log JSON, thông báo khi daemon start hoặc stop (M11).
- `status` thoát mã khác 0 khi còn alert mở (M10).
- Probe: method, header, ngưỡng latency, DNS (M8).
- Quiet hours, xuất metrics (M12).
- `password_file`, pgpass (B15).
- `--limit-download`, `keep-within`, `keep-hourly`, lệnh `unlock` (B9–B11).
- Version cho schema config (B12).

## Ngoài phạm vi

Không đề xuất tính năng AI hay LLM, web UI, central server. Việc đo CPU và RAM của máy chủ vẫn nằm ngoài Sentinel; chỉ thêm lại cảnh báo ổ đĩa đầy vì chính ổ đầy làm hỏng backup.
