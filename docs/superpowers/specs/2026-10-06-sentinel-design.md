# Sentinel — Tool bảo trì server (Giai đoạn 1: CLI/agent)

## Context
Cần một tool nội bộ để bảo trì 1–20 server Linux: giám sát tài nguyên, backup PostgreSQL + file/thư mục lên Google Drive và S3, kiểm tra uptime, cảnh báo qua Telegram / Email / Discord / Slack. Thư mục `/home/workspace/project-backup` hiện trống → dự án mới.

Quyết định đã chốt với người dùng:
- Ngôn ngữ **Go**, một binary duy nhất, chạy như daemon systemd trên mỗi server.
- **Giai đoạn 1 = CLI + daemon**; giai đoạn 2 mới thêm web dashboard trung tâm (ngoài phạm vi plan này, nhưng lưu state vào SQLite để dashboard dùng lại).
- Backup **bọc restic + rclone** (dedup, mã hóa, snapshot; rclone làm backend Google Drive).
- Cảnh báo: Telegram, SMTP, Discord/Slack webhook.

Giả định: chỉ Linux; config YAML; secret lấy qua biến môi trường `${VAR}`.

## Kiến trúc

```
sentinel (binary)
├─ cmd/sentinel/main.go          cobra root
├─ internal/config             load YAML + expand ${ENV}, validate
├─ internal/scheduler          robfig/cron/v3, chạy job, chống chạy chồng (mutex/job)
├─ internal/backup             wrapper restic (exec + --json)
│   ├─ restic.go               Backup / Forget+Prune / Check / Snapshots / Restore
│   ├─ postgres.go             restic backup --stdin-from-command -- pg_dump -Fc ...
│   └─ files.go                restic backup <paths> --exclude ...
├─ internal/monitor            gopsutil: CPU, RAM, swap, disk/mount, load; ngưỡng + thời gian duy trì
├─ internal/uptime             HTTP (status, keyword, hạn TLS cert), TCP, ping (pro-bing; cần CAP_NET_RAW trong systemd unit)
├─ internal/notify             interface Notifier; telegram, smtp, discord, slack; cooldown/dedupe
├─ internal/alert              state machine OK→FIRING→RESOLVED, gửi qua notify
├─ internal/store              SQLite (modernc.org/sqlite, pure Go): backup_runs, checks, metrics, alerts
└─ deploy/                     sentinel.service (systemd), install.sh, .goreleaser.yaml, example config
```

### Lệnh CLI
- `sentinel run` — daemon: scheduler chạy backup / monitor / uptime theo config.
- `sentinel doctor` — kiểm tra restic, rclone, pg_dump có sẵn và đúng phiên bản (restic ≥ 0.17 để có `--stdin-from-command`), repo truy cập được, gửi được notify.
- `sentinel backup run <job>` | `backup list <job>` | `backup restore <job> <snapshot> --target DIR` | `backup init <job>` (restic init repo).
- `sentinel check` — chạy tất cả uptime check một lần, in bảng kết quả.
- `sentinel status` — tóm tắt từ SQLite: lần backup gần nhất, cảnh báo đang mở, metric hiện tại.
- `sentinel notify test` — gửi tin thử tới mọi kênh.

### Config mẫu (`/etc/sentinel/config.yaml`)
```yaml
server_name: web-01
store: /var/lib/sentinel/state.db
notifiers:
  telegram: { token: ${TG_TOKEN}, chat_id: "-100123" }
  smtp: { host: smtp.gmail.com, port: 587, user: ${SMTP_USER}, pass: ${SMTP_PASS}, to: [ops@x.com] }
  discord: { webhook: ${DISCORD_WEBHOOK} }
  slack:   { webhook: ${SLACK_WEBHOOK} }
repositories:
  s3main:  { url: "s3:s3.amazonaws.com/bucket/web-01", password: ${RESTIC_PW}, env: { AWS_ACCESS_KEY_ID: ${AK}, AWS_SECRET_ACCESS_KEY: ${SK} } }
  gdrive:  { url: "rclone:gdrive:backups/web-01", password: ${RESTIC_PW} }
backups:
  - name: appdb
    type: postgres
    schedule: "0 2 * * *"
    postgres: { host: localhost, user: postgres, database: app, password: ${PGPASSWORD} }
    repositories: [s3main, gdrive]
    retention: { daily: 7, weekly: 4, monthly: 6 }
  - name: uploads
    type: files
    schedule: "30 2 * * *"
    paths: [/var/www/app/uploads, /etc/nginx]
    exclude: ["*.tmp"]
    repositories: [s3main]
    retention: { daily: 7, weekly: 4 }
repo_check_schedule: "0 4 * * 0"       # restic check hàng tuần cho mọi repository
monitor:
  interval: 60s
  thresholds: { cpu: 90%/5m, memory: 90%/5m, disk: 85%, load_per_core: 2.0/5m }
uptime:
  - { name: website, type: http, url: https://example.com, interval: 60s, expect_status: 200, keyword: "Welcome", cert_expiry_days: 14, fail_after: 3 }
  - { name: db-port, type: tcp, address: 10.0.0.5:5432, interval: 60s, fail_after: 3 }
```

### Luồng xử lý chính
- **Backup:** scheduler → với mỗi repo: `restic backup ... --json --tag job=<name> --host <server>` → parse summary (số file, dung lượng thêm, snapshot id, thời gian) → `restic forget --prune --keep-*` → ghi `backup_runs` → lỗi thì alert ngay, thành công thì alert ở mức info (tùy chọn). Postgres dùng `--stdin-from-command` để exit code ≠ 0 của pg_dump làm hỏng snapshot (không bị “backup rỗng mà vẫn báo thành công”). Env của restic được truyền qua `cmd.Env`, không truyền qua argv để tránh lộ secret.
- **Cảnh báo backup bị bỏ lỡ:** nếu job không có lần thành công nào trong 2× chu kỳ → alert “stale backup”.
- **Monitor:** lấy mẫu mỗi `interval`, giữ cửa sổ trượt; vượt ngưỡng liên tục đủ thời gian → FIRING; xuống dưới ngưỡng → RESOLVED.
- **Uptime:** `fail_after` lần lỗi liên tiếp → FIRING; lần thành công đầu tiên → RESOLVED (kèm thời gian downtime). Cảnh báo cert sắp hết hạn.
- **Alert/notify:** key = (loại, tên); chỉ gửi khi đổi trạng thái + nhắc lại theo `cooldown` (mặc định 1h). Gửi song song tới các kênh, có timeout/retry; lỗi notify thì ghi log, không làm crash daemon.

### Xử lý lỗi
- `doctor` chạy lúc khởi động `run`: thiếu binary hoặc config sai → thoát với thông báo rõ ràng.
- Mỗi job có timeout (mặc định 6h cho backup, 10s cho check); restic bị lock thì thử lại sau, không unlock tự động.
- Graceful shutdown bằng SIGTERM: chờ job đang chạy (context cancel → kill restic).

## Thư viện
cobra, gopkg.in/yaml.v3, robfig/cron/v3, shirou/gopsutil/v4, prometheus-community/pro-bing, modernc.org/sqlite, log/slog (stdlib), net/smtp (stdlib). Không dùng SDK S3/Drive vì restic + rclone đã lo phần đó.

## Thứ tự triển khai
1. Khung dự án: go mod, cobra, config + validate, slog, store SQLite + migration.
2. notify (4 kênh) + `notify test`.
3. backup: restic wrapper + files + postgres + retention + `backup *` + `doctor`.
4. scheduler + `run` + alert state machine + stale-backup.
5. uptime + `check`.
6. monitor.
7. `status`, systemd unit, install.sh, goreleaser, README (cách cấu hình rclone Google Drive: `rclone config` với service account hoặc OAuth).

## Kiểm thử & xác minh
- Unit test: parse config, parse JSON của restic (fixture output thật), state machine alert, ngưỡng monitor (nguồn metric giả), uptime checker với `httptest`/TCP listener, notifier với `httptest` server.
- Integration (`docker compose` trong `test/`): Postgres + MinIO (S3) + restic thật → `sentinel backup init/run/list/restore appdb` → restore dump và `pg_restore` vào DB mới, so sánh số dòng. Thêm job files → restore → so sánh checksum.
- Kiểm thử tay: `sentinel doctor`; `sentinel notify test` tới Telegram thật; dừng một web service → nhận alert sau `fail_after` lần, bật lại → nhận RESOLVED; `stress` CPU → nhận alert sau 5 phút.
- Google Drive: test tay với rclone remote đã cấu hình (`rclone:gdrive:sentinel-test`).
- `go vet ./... && go test ./...` phải pass.

## Giai đoạn 2 (chỉ định hướng, không làm bây giờ)
Server trung tâm nhận dữ liệu do agent đẩy lên (HTTP + token), web dashboard đọc dữ liệu tương đương các bảng SQLite hiện tại.
