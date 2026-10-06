# Sentinel

Agent bảo trì server viết bằng Go: một binary duy nhất cài trên mỗi server Linux để

- **Backup** PostgreSQL và file/thư mục bằng [restic](https://restic.net) (mã hóa, chống trùng lặp, snapshot) lên **S3** (AWS, MinIO, R2, Wasabi…) và **Google Drive** (qua [rclone](https://rclone.org)), có retention và `restic check` định kỳ.
- **Giám sát** CPU, RAM, load, dung lượng disk, có ngưỡng và thời gian duy trì (ví dụ `90%/5m`).
- **Kiểm tra uptime** HTTP (status, từ khóa, hạn chứng chỉ TLS), TCP, ping.
- **Cảnh báo** qua Telegram, Email (SMTP), Discord, Slack: báo khi lỗi xảy ra, nhắc lại theo `cooldown`, báo khi đã khắc phục. Cảnh báo cả khi một job **quá lâu chưa backup thành công**.
- **Server trung tâm** (`sentinel server`, tùy chọn): web UI quản lý nhiều agent — xem trạng thái/biểu đồ, bấm backup ngay, restore, sửa cấu hình từ xa — không cần mở port trên agent.

Trạng thái được lưu trong SQLite (`/var/lib/sentinel/state.db`); nếu agent kết nối tới server trung tâm, dữ liệu được đẩy lên server để xem trên web.

## Cài đặt

Yêu cầu trên server: restic ≥ 0.17, rclone (nếu dùng Google Drive), `pg_dump` cùng major version với PostgreSQL (nếu backup DB).

```sh
make build                        # -> bin/sentinel (hoặc tải bản release)
scp bin/sentinel deploy/* server:/tmp/sentinel/
ssh server 'cd /tmp/sentinel && sudo sh install.sh ./sentinel'
```

`install.sh` cài restic/rclone nếu chưa có, chép binary vào `/usr/local/bin`, tạo `/etc/sentinel/config.yaml` (từ [config.example.yaml](deploy/config.example.yaml)), file secret `/etc/sentinel/sentinel.env` và systemd unit.

## Cấu hình

Xem [deploy/config.example.yaml](deploy/config.example.yaml); mọi trường đều có chú thích. Secret được viết dạng `${VAR}` và đặt trong `/etc/sentinel/sentinel.env`:

```sh
RESTIC_PASSWORD=mot-mat-khau-dai-va-ngau-nhien
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
PGPASSWORD=...
TG_TOKEN=123456:ABC...
```

> **Mất `RESTIC_PASSWORD` là mất toàn bộ backup.** Hãy lưu nó ở nơi khác ngoài server (ví dụ trình quản lý mật khẩu).

### Google Drive (rclone)

Chạy bằng root (vì service chạy bằng root):

```sh
sudo rclone config
#  n) New remote -> name: gdrive -> storage: drive -> scope: drive.file (1)
#  Server không có trình duyệt: chọn "n" ở bước auto config rồi làm theo
#  hướng dẫn `rclone authorize "drive"` trên máy có trình duyệt.
sudo rclone lsd gdrive:           # kiểm tra
```

Sau đó dùng `url: "rclone:gdrive:backups/<server>"` trong `repositories`. Có thể dùng service account (`service_account_file`) thay cho OAuth nếu dùng Google Workspace.

### Telegram

Tạo bot với @BotFather để lấy token, thêm bot vào nhóm, gửi một tin nhắn rồi mở `https://api.telegram.org/bot<TOKEN>/getUpdates` để lấy `chat.id`.

## Sử dụng

```sh
set -a; . /etc/sentinel/sentinel.env; set +a   # khi chạy tay ngoài systemd

sentinel doctor                     # kiểm tra config, restic/rclone/pg_dump, repository, store
sentinel notify test                # gửi tin thử tới mọi kênh
sentinel backup init appdb          # tạo restic repository (một lần cho mỗi job)
sentinel backup run appdb           # chạy backup ngay
sentinel backup list appdb          # liệt kê snapshot
sentinel backup restore appdb latest --target /tmp/restore [-r s3main]
sentinel check                      # chạy mọi uptime check một lần
sentinel status                     # backup gần nhất, cảnh báo đang mở, metric, uptime

sudo systemctl enable --now sentinel
journalctl -u sentinel -f
```

### Khôi phục PostgreSQL

Backup PostgreSQL được lưu dưới dạng file `<database>.dump` (định dạng custom của `pg_dump`):

```sh
sentinel backup restore appdb latest --target /tmp/restore
pg_restore --clean --if-exists -d app /tmp/restore/app.dump
```

## Server trung tâm (quản lý nhiều agent)

Dùng khi có từ 2 server trở lên và muốn xem/điều khiển tất cả từ một web UI. Agent luôn là bên chủ động kết nối ra ngoài (push báo cáo, long-poll lệnh), nên **không cần mở port nào trên agent**, chạy tốt sau NAT.

```sh
make build
scp bin/sentinel deploy/* server-trung-tam:/tmp/sentinel/
ssh server-trung-tam 'cd /tmp/sentinel && sudo sh install-server.sh ./sentinel'
```

`install-server.sh` chép binary, tạo `/etc/sentinel/server.yaml` (từ [server.example.yaml](deploy/server.example.yaml)), `/etc/sentinel/sentinel.env` và systemd unit `sentinel-server`. Xem chú thích trong file mẫu để cấu hình địa chỉ nghe, TLS (hoặc đặt sau reverse proxy có TLS sẵn) và kênh báo khi agent mất kết nối.

```sh
sentinel server hash-password                 # nhập mật khẩu admin, dán kết quả vào server.yaml
sudo systemctl enable --now sentinel-server
```

Mở UI, đăng nhập, bấm **Add server** để tạo một agent — UI hiện token và đoạn cấu hình cần thêm vào agent đó:

```yaml
central:
  url: https://sentinel.example.com
  token: "${SENTINEL_TOKEN}"
  managed: true     # true: sửa backup/monitor/uptime từ UI; false: agent tự quản lý config, chỉ báo cáo
```

Sau khi thêm vào `/etc/sentinel/config.yaml` và `sentinel.env` của agent rồi `systemctl restart sentinel`, agent sẽ xuất hiện "online" trên UI trong vòng một phút. Với `managed: true`, cấu hình hiện có của agent (trừ `server_name`, `store`, `central`, `restore_dir` — các trường này luôn ở lại agent) được tải lên server làm phiên bản v1, sau đó có thể sửa/rollback từ trang **Configuration** của agent đó.

Trên trang chi tiết agent: nút **Backup now** cho từng job, xem snapshot và **Restore** (phải gõ lại tên job để xác nhận) — file được ghi vào một thư mục mới dưới `restore_dir` của agent (mặc định `/var/lib/sentinel/restore`), không đè lên bất cứ gì.

### Lưu ý

- Nếu job có nhiều repository, mỗi repository được backup riêng (với PostgreSQL thì `pg_dump` chạy một lần cho mỗi repository).
- Nếu `pg_dump` thất bại thì restic không tạo snapshot, và lần chạy được ghi nhận là lỗi.
- Backup có snapshot nhưng không đọc được một số file (restic trả exit code 3) cũng bị tính là **lỗi**.
- Khi repository đang bị khóa, daemon thử lại 3 lần, mỗi lần cách nhau 5 phút. Sentinel không bao giờ tự `unlock`.
- Daemon bị khởi động lại thì không gửi lại các cảnh báo đang mở, nhưng vẫn báo khi chúng được khắc phục.
- Lịch sử uptime và metric được giữ 30 ngày; lịch sử backup được giữ vĩnh viễn.
- Agent nối server trung tâm: nếu mất kết nối, agent vẫn backup/giám sát/cảnh báo bình thường bằng cấu hình đã cache lần cuối; chỉ việc đẩy báo cáo và nhận lệnh/cấu hình mới bị tạm hoãn. Server báo "offline" sau `offline_after` (mặc định 3 phút) không có báo cáo.
- Đổi cấu hình từ UI mà agent đang chạy backup thì agent chờ backup xong rồi mới áp dụng, không ngắt giữa chừng.

## Phát triển

```sh
make test        # unit test (cần restic trong PATH cho test backup, nếu không sẽ skip)
make e2e         # integration test: Postgres + S3 (SeaweedFS) bằng Docker
```

Cấu trúc thư mục:

```
cmd/sentinel/        lệnh CLI (cobra)
internal/config      đọc YAML, ${ENV}, kiểm tra hợp lệ
internal/backup      wrapper restic, job files/postgres, retention
internal/scheduler   daemon: lịch cron, backup quá hạn, restic check
internal/alert       trạng thái cảnh báo OK → FIRING → RESOLVED, cooldown
internal/notify      Telegram, SMTP, Discord, Slack
internal/uptime      kiểm tra HTTP/TCP/ping
internal/monitor     metric hệ thống (gopsutil)
internal/store       SQLite (agent)
internal/api         kiểu dữ liệu dùng chung agent ↔ server
internal/agent       client, đẩy báo cáo, chạy lệnh từ server, đồng bộ config
internal/server      HTTP: agent API, web UI (htmx), service/ chứa logic + SQLite riêng của server
deploy/              systemd units, install*.sh, config mẫu (agent và server)
test/                integration test bằng docker compose
```
