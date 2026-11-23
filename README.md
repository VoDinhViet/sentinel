# Sentinel

Công cụ bảo trì server viết bằng Go: một binary duy nhất cài trên mỗi server Linux để

- **Backup** PostgreSQL, MySQL/MariaDB, MongoDB và file/thư mục bằng [restic](https://restic.net) (mã hóa, chống trùng lặp, snapshot) lên **S3** (AWS, MinIO, R2, Wasabi…) và **Google Drive** (qua [rclone](https://rclone.org)), có retention và `restic check` định kỳ.
- **Kiểm tra uptime** HTTP (status, từ khóa, hạn chứng chỉ TLS), TCP, ping.
- **Cảnh báo** qua Telegram, Email (SMTP), Discord, Slack: báo khi lỗi xảy ra, nhắc lại theo `cooldown`, báo khi đã khắc phục. Cảnh báo cả khi một job **quá lâu chưa backup thành công**.

Mọi thứ điều khiển bằng dòng lệnh (`sentinel backup run|list|restore`, `status`, `check`, `doctor`) và file cấu hình YAML. Trạng thái được lưu trong SQLite (`/var/lib/sentinel/state.db`).

## Cài đặt

Yêu cầu trên server: restic ≥ 0.17, rclone (nếu dùng Google Drive), công cụ dump của engine nếu backup DB: `pg_dump` (cùng major version với PostgreSQL), `mariadb-dump` hoặc `mysqldump` (MySQL/MariaDB), `mongodump` (MongoDB, gói `mongodb-database-tools`). `sentinel doctor` báo công cụ nào còn thiếu.

```sh
make build                        # -> bin/sentinel (cần Go trên máy build; hoặc tải bản release)
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
sentinel status                     # backup gần nhất, cảnh báo đang mở, uptime
sentinel status -o json             # như trên, dạng JSON cho script
sentinel tui                        # dashboard tương tác trên terminal (tab/1-3 đổi mục, ↑↓ chọn, r làm mới, q thoát)

sudo systemctl enable --now sentinel
journalctl -u sentinel -f
```

### Khôi phục PostgreSQL

Backup PostgreSQL được lưu dưới dạng file `<database>.dump` (định dạng custom của `pg_dump`):

```sh
sentinel backup restore appdb latest --target /tmp/restore
pg_restore --clean --if-exists -d app /tmp/restore/app.dump
```

### Khôi phục MySQL/MariaDB và MongoDB

- MySQL/MariaDB lưu file `<database>.sql` (dump `--single-transaction`, kèm routines, triggers, events; cột nhị phân dạng hex). Nạp lại: `mysql <database> < /tmp/restore/<database>.sql`.
- MongoDB lưu file `<database>.archive.gz` (`mongodump --archive --gzip`). Nạp lại: `mongorestore --drop --gzip --archive=/tmp/restore/<database>.archive.gz --nsInclude '<database>.*'`.

Sau mỗi lần `sentinel backup restore`, lệnh nạp lại tương ứng được in ra. Mật khẩu của MySQL và MongoDB không bao giờ nằm trên dòng lệnh: Sentinel ghi một file tạm quyền 0600 (`--defaults-extra-file` / `--config`) rồi xóa ngay sau khi dump xong. Client MariaDB ≥ 11.4 mặc định đòi TLS; nếu server không có TLS, đặt `tls: disabled` trong khối `mysql:`.

## Chạy bằng Docker

Không muốn cài trực tiếp thì dùng image Docker (đã gồm restic, rclone, pg_dump): `make docker` build image `sentinel:latest` từ binary vừa build, rồi `docker save sentinel | ssh vps docker load` (hoặc push lên registry của bạn và đặt `SENTINEL_IMAGE`). Compose mẫu: [docker-compose.agent.yml](deploy/docker-compose.agent.yml) cho agent (backup, uptime check, cảnh báo). Mount các đường dẫn cần backup (chỉ đọc) vào đúng đường dẫn trong config; `network_mode: host` để uptime check và `pg_dump` tới được dịch vụ trên VPS.

### Lưu ý

- Mục `monitor:` trong config cũ vẫn được chấp nhận nhưng bị bỏ qua (có cảnh báo trong log): Sentinel không còn đo CPU/RAM/disk. Dùng công cụ giám sát riêng cho việc đó.
- Nếu job có nhiều repository, mỗi repository được backup riêng (với PostgreSQL thì `pg_dump` chạy một lần cho mỗi repository).
- Nếu `pg_dump` thất bại thì restic không tạo snapshot, và lần chạy được ghi nhận là lỗi.
- Backup có snapshot nhưng không đọc được một số file (restic trả exit code 3) cũng bị tính là **lỗi**.
- Khi repository đang bị khóa, daemon thử lại 3 lần, mỗi lần cách nhau 5 phút. Sentinel không bao giờ tự `unlock`.
- Daemon bị khởi động lại thì không gửi lại các cảnh báo đang mở, nhưng vẫn báo khi chúng được khắc phục.
- Lịch sử uptime được giữ 30 ngày; lịch sử backup được giữ vĩnh viễn.

## Phát triển

```sh
make test        # golangci-lint + govulncheck + go vet + go test (cần restic trong PATH cho test backup, nếu không sẽ skip)
make e2e         # integration test: Postgres + S3 (SeaweedFS) bằng Docker 
make e2e-engines # integration test: MariaDB 10.11, MySQL 5.7, MongoDB 5.0 (chỉ cần docker)
```

Cấu trúc thư mục:

```
cmd/sentinel/        lệnh CLI (cobra)
internal/config      đọc YAML, ${ENV}, kiểm tra hợp lệ
internal/backup      wrapper restic, job files/database, retention
internal/scheduler   daemon: lịch cron, backup quá hạn, restic check
internal/alert       trạng thái cảnh báo OK → FIRING → RESOLVED, cooldown
internal/notify      Telegram, SMTP, Discord, Slack
internal/uptime      kiểm tra HTTP/TCP/ping
internal/store       SQLite (trạng thái, lịch sử backup/uptime)
deploy/              systemd units, install*.sh, config mẫu 
test/                integration test bằng docker compose
```
