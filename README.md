# Sentinel

Công cụ bảo trì server viết bằng Go: một binary duy nhất cài trên mỗi server Linux để

- **Backup** PostgreSQL, MySQL/MariaDB, MongoDB và file/thư mục bằng [restic](https://restic.net) (mã hóa, chống trùng lặp, snapshot) lên **S3** (AWS, MinIO, R2, Wasabi…) và **Google Drive** (qua [rclone](https://rclone.org)), có retention và `restic check` định kỳ.
- **Kiểm tra uptime** HTTP (status, từ khóa, hạn chứng chỉ TLS), TCP, ping.
- **Cảnh báo** qua Telegram, Email (SMTP), Discord, Slack: báo khi lỗi xảy ra, nhắc lại theo `cooldown`, báo khi đã khắc phục. Cảnh báo cả khi một job **quá lâu chưa backup thành công**.

Mọi thứ điều khiển bằng dòng lệnh (`sentinel backup run|list|restore`, `status`, `check`, `doctor`) và file cấu hình YAML. Trạng thái được lưu trong SQLite (`/var/lib/sentinel/state.db`).

## Cài đặt

Trên Ubuntu/Debian, một dòng lệnh (cần quyền root) cài Sentinel cùng mọi công cụ nó chạy: restic, rclone, `pg_dump`, `mariadb-dump`, `mongodump`, rồi tạo `/etc/sentinel/config.yaml`, `/etc/sentinel/sentinel.env` và systemd unit:

```sh
curl -fsSL https://raw.githubusercontent.com/VoDinhViet/sentinel/main/deploy/install.sh | sudo sh
```

Bản phát hành được tải về và kiểm tra SHA-256 trước khi cài. Chạy lại cùng lệnh để nâng cấp: binary được thay, `config.yaml` và `sentinel.env` giữ nguyên. Tùy chọn (thêm sau `sh -s --`): `--version v1.2.0` cài đúng một bản, `--no-mongo` bỏ `mongodump`, `--no-db-clients` bỏ client PostgreSQL/MariaDB; biến `SENTINEL_REPO` đổi repo GitHub tải về. Client PostgreSQL lấy từ apt, nên cần cùng major version với PostgreSQL server (`sentinel doctor` báo khi thiếu).

Bản phát hành do GitHub Actions tạo khi đẩy tag `v*` (`.github/workflows/release.yml`, goreleaser). Chưa có bản nào thì dòng lệnh trên báo "could not download" và dừng; khi đó build tay:

```sh
make build                        # -> bin/sentinel (dùng Go nếu có, không thì build qua docker)
scp bin/sentinel deploy/* server:/tmp/sentinel/
ssh server 'cd /tmp/sentinel && sudo sh install.sh ./sentinel'
```

Sau khi cài: sửa `/etc/sentinel/config.yaml` và `sentinel.env`, rồi `sentinel doctor`, `sentinel backup init <job>`, `systemctl enable --now sentinel`.

## Chạy trên terminal của bạn

Không cần root, không cần service: build, cài vào `~/.local/bin`, rồi thao tác backup ngay trong terminal.

```sh
make build && make install          # không có Go thì tự build qua docker; sudo make install PREFIX=/usr/local để cài cho mọi người dùng
sentinel init                       # một form: kho lưu ở đâu, backup gì, lịch nào -> ghi ~/.config/sentinel/config.yaml
export RESTIC_PASSWORD='mat-khau-dai-va-kho-doan'   # mất mật khẩu này là mất backup
sentinel backup init documents      # tạo kho restic (một lần)
sentinel backup run documents       # backup ngay
sentinel manage                     # màn hình quản lý: backup, snapshot + restore, sửa mọi thứ trong config
```

`sentinel init` chạy bằng root thì ghi `/etc/sentinel/config.yaml` và tự tạo mật khẩu ngẫu nhiên cho kho trong `/etc/sentinel/sentinel.env` (quyền 0600; hãy lưu một bản ở nơi khác). Đã có config thì lệnh từ chối, trừ khi thêm `--force` (file cũ được giữ lại thành `config.yaml.old`). Thích sửa tay thì chép [deploy/config.user.example.yaml](deploy/config.user.example.yaml).

Không cần gõ `-c`: Sentinel lấy file config theo thứ tự `--config`, biến `SENTINEL_CONFIG`, `/etc/sentinel/config.yaml`, rồi `~/.config/sentinel/config.yaml` (file đầu tiên tồn tại). Nếu `~/.local/bin` chưa nằm trong `PATH`, `make install` sẽ nhắc.

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

### Lịch chạy và giá trị mặc định

`schedule` nhận cron 5 trường như trước (`"0 2 * * *"`, `@daily`, `@every 1h`) và các dạng dễ đọc hơn:

| Viết | Nghĩa |
|---|---|
| `daily 02:00` | mỗi ngày lúc 02:00 |
| `weekly sun 03:00` | mỗi Chủ nhật lúc 03:00 |
| `monthly 1 04:00` | ngày 1 hằng tháng lúc 04:00 (ngày 1-28) |
| `hourly`, `hourly :15` | đầu mỗi giờ, hoặc phút 15 mỗi giờ |
| `every 6h` | mỗi 6 giờ (tối thiểu 1m) |
| `manual` hoặc để trống | chỉ chạy tay |

Khối `defaults:` cho các job không ghi riêng `schedule`, `repositories` hoặc `retention`, nên file không phải lặp lại:

```yaml
defaults:
  schedule: daily 02:00
  repositories: [s3main, gdrive]
  retention: { daily: 7, weekly: 4, monthly: 6 }
```

Giá trị ghi trong job luôn thắng. Config chỉ có **một** kho thì job không cần `repositories:`; có nhiều kho mà không có `defaults.repositories` thì Sentinel báo lỗi và liệt kê tên các kho. Khi đã đặt `defaults.retention`, một job không thể xin "giữ tất cả" (job không ghi retention sẽ nhận giá trị mặc định).

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

sentinel init                       # tạo config đầu tiên bằng một form (--force để thay config cũ)
sentinel doctor                     # kiểm tra config, restic/rclone/pg_dump, repository, store
sentinel notify test                # gửi tin thử tới mọi kênh
sentinel backup init appdb          # tạo restic repository (một lần cho mỗi job)
sentinel backup run appdb           # chạy backup ngay
sentinel backup list appdb          # liệt kê snapshot
sentinel backup restore appdb latest --target /tmp/restore [-r s3main]
sentinel check                      # chạy mọi uptime check một lần
sentinel status                     # backup gần nhất, cảnh báo đang mở, uptime
sentinel status -o json             # như trên, dạng JSON cho script
sentinel manage                     # màn hình quản lý tương tác (alias: tui): trạng thái, backup ngay, snapshot + restore, sửa mọi thứ trong config, Doctor, ? trợ giúp, q thoát

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

### Màn hình quản lý (`sentinel manage`)

Chạy trong terminal, năm tab: **Backups**, **Uptime**, **Alerts**, **Doctor**, **Config** (phím `1`-`5`). Dòng tóm tắt dưới tiêu đề cho biết ngay cần chú ý gì (ví dụ `1 failed · 1 alert · 1 warning`) hoặc chỉ `all good`; mỗi dòng có chấm màu theo trạng thái, thời gian dạng tương đối (`1h ago`), và cột **HISTORY** là các lần chạy gần nhất của job (`✓✓✗✓`, mới nhất ở bên phải). Dòng đang chọn được tô nền xanh. Màn hình tự làm mới mỗi 5 giây, `r` làm mới ngay.

**Chuột dùng được cho mọi thứ**: bấm tab, bấm một dòng để chọn (bấm lần nữa để mở: snapshot ở tab Backups, form sửa ở tab Config), bấm các phím ở dòng cuối (`b backup`, `? help`...), con lăn để cuộn; trong form bấm vào ô để nhập, bấm `‹ ›` để đổi lựa chọn, bấm ô tick để chọn kho, bấm `[ Save ]` `[ Cancel ]`. Bàn phím vẫn đủ: `?` hiện các phím của màn hình đang mở. Trong `tmux` cần `set -g mouse on`.

Tab **Backups**: `b` backup ngay job đang chọn (khóa job vẫn có hiệu lực nên không chạy chồng với daemon), `s` xem snapshot của job rồi restore (nhập thư mục đích tuyệt đối và gõ lại tên job để xác nhận), `ctrl+c` hủy tác vụ đang chạy. `a` thêm, `e` sửa, `d` xóa job (files hoặc database) ngay trong `config.yaml`.

Tab **Doctor** chạy các kiểm tra của `sentinel doctor` (config, restic/rclone/dump tool, store, từng repository, kênh cảnh báo) ngầm ở lần mở đầu tiên: kho không trả lời có thể mất tới vài chục giây nên màn hình vẫn dùng được. `r` chạy lại; dòng đang chọn hiện gợi ý cách sửa, và nhãn tab đỏ khi có lỗi.

Tab **Config** liệt kê mọi thứ trong file config: kho (repositories), job, uptime check, kênh cảnh báo và thiết lập chung. `a` thêm vào nhóm của dòng đang chọn, `e` hoặc `enter` sửa, `d` xóa (gõ lại tên để xác nhận; kho đang có job dùng thì không xóa được và màn hình nêu tên các job đó).

Các form giúp nhập đúng ngay từ đầu: kho chọn bằng ô tick, kiểu kho (thư mục, S3, rclone, sftp) tự ghép thành `url`, lịch có các mẫu có sẵn (`←` `→`) kèm dòng giải thích bằng chữ và lần chạy kế tiếp (`→ every day at 02:00 · next Tue 02:00 (in 13h)`), retention là năm ô số, đường dẫn của job files được báo nếu không thấy trên máy này. Lỗi hiện ngay dưới ô sai.

Sửa bằng form giữ nguyên comment, thứ tự và các khóa không quản lý trong file; chỉ ghi những giá trị bạn đặt (giá trị mặc định thì không ghi ra); bản cũ được lưu ở `config.yaml.bak` cùng quyền truy cập. **Mật khẩu và token không bao giờ nhập vào màn hình này**: chỉ nhập *tên* biến môi trường (ví dụ `PG_APPDB_PASSWORD`), config ghi `${PG_APPDB_PASSWORD}` và giá trị thật nằm trong `/etc/sentinel/sentinel.env`; một giá trị viết thẳng trong file được giữ nguyên cho tới khi bạn nhập tên biến thay thế. Sau khi sửa, daemon đang chạy phải được khởi động lại (`systemctl restart sentinel`) để nhận thay đổi.

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
