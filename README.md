# Sentinel

Một binary Go cài trên mỗi server Linux: **backup** file và database bằng restic, **kiểm tra uptime**, và **cảnh báo** khi có chuyện. Điều khiển bằng dòng lệnh, file cấu hình YAML và một màn hình quản lý trong terminal.

![Màn hình sentinel manage: các job backup, kết quả và lịch sử từng lần chạy](docs/images/manage-backups.png)

- **Backup** PostgreSQL, MySQL/MariaDB, MongoDB và file/thư mục bằng [restic](https://restic.net) (mã hóa, chống trùng lặp, snapshot) lên thư mục, S3 (AWS, MinIO, R2, Wasabi…), sftp hoặc Google Drive (qua [rclone](https://rclone.org)); có retention và `restic check` hằng tuần (mặc định chủ nhật 04:00; đổi bằng `verify_schedule`, tắt bằng `verify_schedule: manual`).
- **Uptime** HTTP (status, từ khóa, hạn chứng chỉ TLS), TCP, ping.
- **Cảnh báo** qua Telegram, Email (SMTP), Discord, Slack: báo khi lỗi, nhắc lại theo `cooldown`, báo khi đã khắc phục, báo khi một job quá lâu chưa backup thành công, và báo khi ổ đĩa đang chứa state, cache restic hoặc repository local còn trống dưới 10% (đổi bằng `disk.min_free_percent`, `0` để tắt). Tin không gửi được tới kênh nào được lưu lại và gửi lại sau tối đa 5 phút.
- **Heartbeat**: lỗi duy nhất Sentinel không tự báo được là chính daemon dừng. Đặt `heartbeat.url` (healthchecks.io, push monitor của Uptime Kuma; chỉ https): daemon gọi URL đó lúc khởi động rồi mỗi `heartbeat.every` (mặc định 5m), dịch vụ bên ngoài báo cho bạn khi các lần gọi ngừng. `sentinel doctor` nhắc nếu chưa đặt.

- **HTTP API** (tùy chọn, mặc định tắt): đặt `api.listen` và `api.token`, daemon mở API JSON để xem trạng thái, liệt kê snapshot, bắt đầu hoặc dừng backup từ máy khác hoặc từ script. Xem mục [HTTP API](#http-api).

Sentinel không có web UI, không có server trung tâm và không lưu lịch sử CPU/RAM và không cảnh báo về chúng (tab **Server** chỉ cho xem tình trạng máy lúc này; cần theo dõi dài hạn thì dùng công cụ giám sát riêng; Sentinel chỉ cảnh báo khi ổ đĩa sắp đầy). Phiên bản hiện tại: v0.1.0.

## Cài đặt

**Trên server Ubuntu/Debian** (cần root). Repo [`Cong-ty-ThoMi-Group/gm-backup`](https://github.com/Cong-ty-ThoMi-Group/gm-backup) là private nên không có lệnh `curl ... | sh` tải thẳng; cài từ bản clone (cần tài khoản có quyền đọc repo). Script `deploy/install.sh` cài Sentinel cùng restic, rclone, `pg_dump`, `mariadb-dump`, `mongodump`, rồi tạo `/etc/sentinel/config.yaml`, `/etc/sentinel/sentinel.env` và systemd unit:

```sh
git clone https://github.com/Cong-ty-ThoMi-Group/gm-backup.git && cd gm-backup
make build                           # cần Go hoặc docker
sudo ./deploy/install.sh ./bin/sentinel
```

Server không có Go hoặc docker thì build ở máy khác: `make dist` tạo `dist/sentinel_linux_amd64.tar.gz` (và `arm64`); chép file sang server rồi

```sh
tar -xzf sentinel_linux_amd64.tar.gz && sudo ./deploy/install.sh ./sentinel
```

Mọi thứ script tải về (restic, rclone, mongodump, và Sentinel khi chạy với `--version`) đều được kiểm tra SHA-256 trước khi cài, và chỉ tải qua `https://` (`SENTINEL_BASE_URL` cũng phải là `https://`).

Để nâng cấp: `git pull`, build lại, chạy lại đúng lệnh cài (`config.yaml` và `sentinel.env` được giữ nguyên). Thêm sau đường dẫn binary: `--no-mongo` bỏ `mongodump`, `--no-db-clients` bỏ client PostgreSQL/MariaDB. Client PostgreSQL phải cùng major version với server (`sentinel doctor` báo khi thiếu).

**Nâng cấp và rollback.** Để nâng cấp: `git pull`, build lại, chạy lại đúng lệnh cài (`config.yaml` và `sentinel.env` được giữ nguyên). Script giữ binary cũ ở `/usr/local/bin/sentinel.prev` và restart service `sentinel` nếu nó đang chạy (thêm `--no-restart` để tự restart sau). Restart không chặn: daemon cho backup đang chạy xong (tối đa `shutdown_wait`) rồi mới dừng, trong lúc đó lịch và uptime check tạm ngưng. Rollback:

```sh
sudo install -m 0755 /usr/local/bin/sentinel.prev /usr/local/bin/sentinel && sudo systemctl restart sentinel
```

Chỉ khi bản cũ dừng với lỗi `written by a newer sentinel` (nó không mở file trạng thái mà bản mới hơn đã đổi cấu trúc), dời file `store:` (mặc định `/var/lib/sentinel/state.db`, cùng `-wal` và `-shm`) sang chỗ khác rồi restart: lịch sử chạy và trạng thái cảnh báo bắt đầu lại từ trống, còn dữ liệu backup trong repository không bị ảnh hưởng.

Sau khi cài: sửa `/etc/sentinel/config.yaml` và `sentinel.env`, rồi

```sh
sentinel doctor                      # kiểm tra config, công cụ, kho backup
sentinel backup init appdb           # tạo kho restic (một lần cho mỗi job)
sudo systemctl enable --now sentinel
```

**Trên máy của bạn** (không cần root, không cần service). Cần Go hoặc docker để build:

```sh
make build && make install           # cài vào ~/.local/bin
sentinel init                        # một form: lưu ở đâu, backup gì, lịch nào, và (tuỳ chọn) kênh cảnh báo Telegram, Discord hoặc Slack -> ~/.config/sentinel/config.yaml
sentinel backup run documents        # backup ngay
sentinel manage                      # màn hình quản lý
```

Gõ `sentinel` trơn trên terminal thì mở `init` nếu chưa có config, đã có thì mở `sentinel manage`. Config được lấy theo thứ tự: `--config`, biến `SENTINEL_CONFIG`, `/etc/sentinel/config.yaml`, `~/.config/sentinel/config.yaml`.

## Cấu hình

Mẫu đầy đủ, có chú thích từng trường: [deploy/config.example.yaml](deploy/config.example.yaml) (server) và [deploy/config.user.example.yaml](deploy/config.user.example.yaml) (máy cá nhân).

Mật khẩu và token không viết thẳng vào config mà viết dạng `${TÊN_BIẾN}`; giá trị thật nằm trong `/etc/sentinel/sentinel.env` (quyền 0600):

```sh
RESTIC_PASSWORD=mot-mat-khau-dai-va-ngau-nhien
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
TG_TOKEN=123456:ABC...
```

> **Mất `RESTIC_PASSWORD` là mất toàn bộ backup.** Hãy lưu nó ở nơi khác ngoài server.

Trên terminal, nếu thiếu một biến, Sentinel hỏi giá trị bằng ô nhập ẩn và có thể lưu vào `sentinel.env`. Daemon và script không bao giờ hỏi: thiếu biến là lỗi.

### Lịch chạy

| Viết | Nghĩa |
|---|---|
| `daily 02:00` | mỗi ngày lúc 02:00 |
| `weekly sun 03:00` | mỗi Chủ nhật lúc 03:00 |
| `monthly 1 04:00` | ngày 1 hằng tháng lúc 04:00 |
| `hourly`, `hourly :15` | đầu mỗi giờ, hoặc phút 15 mỗi giờ |
| `every 6h` | mỗi 6 giờ (tối thiểu 1m) |
| `manual` hoặc để trống | chỉ chạy tay |

Cron 5 trường (`"0 2 * * *"`) vẫn dùng được. Khối `defaults:` đặt sẵn `schedule`, `repositories`, `retention` cho các job không ghi riêng:

```yaml
defaults:
  schedule: daily 02:00
  repositories: [s3main, gdrive]
  retention: { daily: 7, weekly: 4, monthly: 6 }
```

Mỗi backup chỉ `forget` các snapshot hết hạn giữ; dung lượng của chúng được trả lại khi daemon chạy `restic prune` trên từng repository, mặc định mỗi tuần một lần (`prune_schedule: weekly sat 03:00`, đặt `manual` để tắt). Prune đọc cả repository và giữ khoá độc quyền nên không chạy sau mỗi backup. Backup chạy bằng CLI (`backup run`, kể cả `--detach`) không có daemon đứng sau nên vẫn prune ngay sau retention như trước. Prune lỗi thì có cảnh báo `prune:<repository>`.

### Google Drive

Chạy bằng root (vì service chạy bằng root):

```sh
sudo rclone config     # n) New remote -> name: gdrive -> storage: drive -> scope: drive.file
sudo rclone lsd gdrive:
```

Server không có trình duyệt: ở bước auto config chọn `n` rồi làm theo `rclone authorize "drive"` trên máy có trình duyệt. Sau đó dùng `url: "rclone:gdrive:backups/<server>"` trong `repositories`.

### Telegram

Tạo bot với @BotFather để lấy token, thêm bot vào nhóm, gửi một tin nhắn rồi mở `https://api.telegram.org/bot<TOKEN>/getUpdates` để lấy `chat.id`. Thử bằng `sentinel notify test`.

## Các lệnh hay dùng

```sh
sentinel doctor                      # kiểm tra config, restic/rclone/pg_dump, kho, nơi lưu trạng thái
sentinel backup run appdb            # chạy backup ngay
sentinel backup run appdb --detach   # chạy nền ở tiến trình riêng, thoát terminal cũng không dừng
sentinel backup stop appdb           # dừng backup đang chạy của job
sentinel backup list appdb           # liệt kê snapshot
sentinel backup restore appdb latest --target /tmp/restore [-r s3main]
sentinel check                       # chạy mọi uptime check một lần
sentinel status                      # daemon có đang chạy không, backup gần nhất, cảnh báo đang mở, uptime
sentinel status -o json              # như trên, cho script
sentinel notify test                 # gửi tin thử tới mọi kênh cảnh báo
journalctl -u sentinel -f            # log của daemon
```

Backup chạy tay (`backup run`, `sentinel manage`, API) cũng đi qua cảnh báo: khi daemon đang chạy, trong khoảng một phút nó gửi cảnh báo nếu lần chạy đó lỗi, và đóng cảnh báo `backup:` đang mở nếu lần chạy đó thành công. Không có daemon thì không ai gửi cảnh báo; kết quả vẫn hiện ở terminal và trong `sentinel status`.

### Dùng trong script hoặc với AI agent

`status`, `doctor`, `check`, `backup list` và `backup run` có `-o json`: JSON ra stdout, tiến độ và log ra stderr, exit code 0 khi mọi thứ ổn và 1 khi có lỗi (JSON vẫn được in đủ trước khi trả lỗi). Công cụ như Claude Code hay Codex gọi thẳng các lệnh này qua shell, không cần MCP.

```sh
sentinel doctor -o json | jq '.ok, (.findings[] | select(.status != "ok"))'
sentinel check -o json  | jq '.checks[] | select(.ok | not)'
sentinel backup list appdb -o json | jq '.repositories[].snapshots[0]'
sentinel backup run appdb -o json   # {job, runs: [{repository, success, result, snapshot_id, bytes_added, duration_seconds}]}
sentinel backup run appdb --detach -o json   # {job, pid, log}
```

`sentinel manage` và `sentinel init` là màn hình tương tác, script không dùng được.

Chạy tay ngoài systemd thì nạp biến trước: `set -a; . /etc/sentinel/sentinel.env; set +a`.

## HTTP API

Tắt theo mặc định. Bật bằng khối `api:` trong config, rồi chạy lại daemon (`sentinel run`):

```yaml
api:
  listen: 127.0.0.1:9311
  token: ${SENTINEL_API_TOKEN}   # giá trị (từ 16 ký tự) để trong sentinel.env
```

Token bắt buộc, kể cả khi chỉ nghe trên `127.0.0.1`. Mọi route `/api/` cần header `Authorization: Bearer <token>`; `/healthz` thì không cần. Token đi qua mạng không mã hóa, nên chỉ nghe trên `127.0.0.1` và truy cập qua SSH tunnel hoặc reverse proxy có TLS. `sentinel doctor` cảnh báo nếu API nghe trên địa chỉ khác máy này.

| Route | Việc |
|---|---|
| `GET /healthz` | daemon còn sống |
| `GET /api/v1/status` | cùng JSON với `sentinel status --output json` |
| `GET /api/v1/alerts` | các cảnh báo đang mở |
| `GET /api/v1/jobs` | các cặp job và repository trong config |
| `GET /api/v1/jobs/{job}/snapshots?repository=<tên>` | snapshot của job, mới nhất trước |
| `POST /api/v1/jobs/{job}/backups[?repository=<tên>]` | chạy backup nền, trả `202` kèm pid và đường dẫn log |
| `DELETE /api/v1/jobs/{job}/backups/running` | yêu cầu backup đang chạy dừng |
| `GET /api/v1/doctor` | kết quả `sentinel doctor` |

Lỗi trả về dạng `{"error": "..."}`: `404` khi job hoặc repository không có, `409` khi job đang chạy (hoặc không có gì để dừng). Lỗi khác chỉ ghi vào log của daemon. Restore và sửa config chưa có qua API; dùng `sentinel manage`.

```sh
curl -H "Authorization: Bearer $SENTINEL_API_TOKEN" http://127.0.0.1:9311/api/v1/status
```

## Khôi phục dữ liệu

`sentinel backup restore` đưa snapshot ra thư mục `--target`, rồi in lệnh để nạp lại vào database:

```sh
# PostgreSQL: file <database>.dump
pg_restore --clean --if-exists -d app /tmp/restore/app.dump

# MySQL/MariaDB: file <database>.sql
mysql shop < /tmp/restore/shop.sql

# MongoDB: file <database>.archive.gz
mongorestore --drop --gzip --archive=/tmp/restore/crm.archive.gz --nsInclude 'crm.*'
```

Job `files` thì file nằm ngay trong `--target`.

`--target` phải là đường dẫn tuyệt đối. Thư mục chưa có sẽ được tạo với quyền `0700`; thư mục có sẵn phải thuộc user đang chạy, không phải symlink và không cho group hoặc người khác ghi, vì restic ghi qua bất cứ thứ gì nó thấy ở đó với quyền của user này (thường là root). Đích là `/` hoặc thư mục hệ thống (`/etc`, `/var`, `/home`, thư mục dữ liệu database...) bị từ chối trừ khi thêm `--force`; `--force` cũng bỏ kiểm tra chủ sở hữu và quyền của thư mục đích, cho mount NFS/CIFS hay thư mục của user khác khi chạy qua `sudo`.

## Màn hình quản lý

`sentinel manage` mở trong terminal với sáu tab (phím `1`-`6`): **Backups**, **Uptime**, **Alerts**, **Doctor**, **Config**, **Server**. Màn hình chia thành các khung (panel):

- **Hàng khung trên cùng** (màn hình từ 110 cột, 28 dòng): *tổng quan* (số job ổn / cảnh báo / lỗi, alert đang mở, số repository và dung lượng), *đang chạy* (thanh tiến độ của backup đang chạy, hoặc job sẽ chạy kế tiếp) và *uptime* (mỗi check một dòng, kèm biểu đồ độ trễ các lần gần nhất; lần lỗi là `✗`). Khung nào có vấn đề thì viền đỏ. Màn hình nhỏ hơn chỉ có một dòng tóm tắt.
- **Khung bảng** với tên các tab trên viền (tab có vấn đề tô đỏ, số bên cạnh là số dòng, riêng Doctor là số lỗi và cảnh báo). Cột LỊCH SỬ là các lần chạy gần nhất của job (`✓✓✗✓`, mới nhất ở bên phải), cột LẦN TỚI là lần chạy theo lịch kế tiếp. Job lỗi hoặc đang chạy có thêm một dòng mờ `└ …` ngay dưới (lỗi, hoặc dung lượng, tốc độ, thời gian còn lại).
- **Khung chi tiết** dưới bảng: dòng đang chọn dạng nhãn và giá trị (loại, nguồn, lịch, lần tới, giữ bao nhiêu bản…), kết quả hoặc lỗi đầy đủ, và kết quả của thao tác vừa làm.
- **Tab Config** chia theo mục đích: *Repository* (nơi lưu backup), *Job*, *Uptime check*, *Notifier* (nơi gửi cảnh báo) và *Cài đặt chung*, mỗi nhóm có cột riêng. Nhóm chưa có gì hiện một dòng mờ `+ … bấm a để thêm`. Dòng phím ở dưới cùng chỉ hiện phím dùng được cho mục đang chọn (ví dụ `t gửi thử` chỉ có ở Notifier).

- **Tab Server** (chỉ đọc) cho biết máy đang thế nào: *Hệ thống* (hostname, hệ điều hành, kernel, uptime, load, CPU, RAM, swap), *Ổ đĩa* (dung lượng trống của từng ổ thật; đỏ khi dưới `disk.min_free_percent`, vàng khi dưới gấp đôi mức đó) và *Dịch vụ* (trạng thái `sentinel.service` và các systemd unit đang `failed`; không hiện trên máy không có systemd). Số liệu chỉ đọc khi tab đang mở (đọc lại mỗi 5 giây hoặc bấm `r`), không lưu lại và không gửi cảnh báo. Load từ 1 CPU trở lên thì vàng, từ 2 thì đỏ; RAM khả dụng dưới 10% vàng, dưới 5% đỏ; swap dùng quá 50% vàng.

Màn hình tự làm mới mỗi 5 giây (2 giây khi có backup đang chạy); chuột dùng được cho mọi thứ (trong `tmux` cần `set -g mouse on`); bấm `?` để xem các phím của màn hình đang mở.

| Phím | Việc |
|---|---|
| `b` | backup ngay job đang chọn, **chạy nền** (đóng manage cũng không dừng) và không chạy chồng với daemon |
| `x` | dừng backup đang chạy của dòng đang chọn (bấm hai lần để xác nhận) |
| `s` | xem snapshot của job và restore |
| `a` `e` `d` | thêm, sửa, xóa job, kho, uptime check, kênh cảnh báo trong `config.yaml` |
| `t` | (tab Config, dòng Notifier) gửi tin thử qua kênh đó; đọc kênh từ file config nên không cần khởi động lại, và chỉ cần các biến của chính kênh đó |
| `ctrl+t` | (trong form job database) thử kết nối: chạy đúng công cụ dump với host, cổng, user, mật khẩu của form rồi dừng ngay khi nó bắt đầu ghi (chỉ đọc), hiện `✓ kết nối được` hoặc lý do lỗi của công cụ; cũng là nút `[ Kết nối thử ]` dưới ô mật khẩu |
| `r` | làm mới / chạy lại Doctor / đọc lại tình trạng máy (tab Server) |

![Form sửa job: lịch chạy được giải thích bằng chữ kèm ba lần chạy kế tiếp](docs/images/manage-job-form.png)

Form sửa giữ nguyên comment và thứ tự trong `config.yaml`; bản cũ được lưu thành `config.yaml.bak`. Mật khẩu và token không nhập ở đây, chỉ nhập *tên* biến môi trường. Sửa xong, daemon đang chạy cần `systemctl restart sentinel` để nhận thay đổi.

## Database lớn: tiến độ, chạy nền, dừng, tài nguyên

Với database vài chục GB, một lần backup mất hàng giờ. Sentinel lo bốn việc:

- **Thấy tiến độ.** Backup đang chạy hiện ở tab Backups (`⏳ ~37%`, dung lượng đã dump, tốc độ, thời gian còn lại, đã chạy bao lâu) và trong `sentinel status` (mục "Đang chạy"). Dump database đi qua stdin nên restic không biết tổng dung lượng: `%` là **ước lượng theo dung lượng lần backup thành công trước** (nên có dấu `~`, không bao giờ quá 99% trước khi xong). Chưa có lần trước thì chỉ hiện dung lượng đã dump và tốc độ. Cảnh báo hiện ra khi 10 phút không có dữ liệu mới, hoặc khi với tốc độ này backup sẽ vượt `timeout` của job.
- **Không bị ngắt.** `b` trong `sentinel manage` và `sentinel backup run <job> --detach` (`-d`) chạy backup ở **tiến trình riêng** (phiên riêng, log ở `<thư mục state>/logs/backup-<job>.log`), nên thoát manage, đóng terminal hay đứt SSH không làm backup dừng. Mở lại manage lúc nào cũng thấy tiến độ. Hai nơi không chạy chồng một job (khoá `flock`: daemon, CLI và manage dùng chung). Server reboot giữa chừng thì backup tách riêng **không tự chạy lại**; lần chạy theo lịch kế tiếp của daemon vẫn chạy.
- **Dừng giữa chừng.** `x` trong manage (hai lần) hoặc `sentinel backup stop <job>` dừng backup đang chạy, ở bất cứ tiến trình nào. Lần dừng không gửi cảnh báo, nhưng vẫn ghi vào lịch sử là `stopped by the user`.
- **Không chiếm server.** Mặc định restic và công cụ dump chạy với CPU `nice 10` và ưu tiên đĩa thấp nhất, mỗi lúc chỉ **một** backup chạy (job khác chờ đến lượt, trên cả daemon lẫn lệnh gõ tay), bản thân sentinel dùng tối đa 2 core. Chỉnh trong `config.yaml` hoặc tab Config → Settings:

```yaml
resources:
  priority: low          # low (mặc định) hoặc normal
  max_parallel: 1        # số backup chạy cùng lúc
  upload_limit_kib: 20480  # tuỳ chọn, KiB/s cho mỗi backup
shutdown_wait: 2h        # xem bên dưới
```

**Timeout.** Một lần backup được chạy tối đa `timeout` của job (mặc định 6h; ô *Timeout* trong "Tuỳ chọn nâng cao" của form job). Backup qua stdin bị cắt (timeout, dừng tay, mất điện) thì **phải làm lại từ đầu**, restic không để lại snapshot dở; với database rất lớn hãy đặt `timeout` rộng hơn thời gian backup thường lệ.

**Khi dừng hoặc restart daemon.** Daemon ngừng nhận lịch mới nhưng **chờ backup đang chạy xong** tối đa `shutdown_wait` (mặc định 2h) rồi mới huỷ; gửi tín hiệu dừng lần hai (Ctrl+C lần nữa) thì huỷ ngay. Trong lúc chờ, lịch (kể cả uptime check) tạm ngưng, nên `systemctl restart sentinel` khi đang có backup lớn có thể mất tới 2 giờ và có khoảng trống theo dõi uptime. `deploy/sentinel.service` đặt `TimeoutStopSec=2h30m`; nếu đổi `shutdown_wait` thì giữ `TimeoutStopSec` lớn hơn nó. Backup chạy tách riêng không bị ảnh hưởng bởi restart daemon.

**Ngoài phạm vi.** `restore` database lớn vẫn chạy trong tiến trình manage hoặc CLI, chưa có tiến độ và chưa chạy nền.

## Chạy bằng Docker

`make docker` build image `sentinel:latest` (đã gồm restic, rclone, pg_dump), rồi dùng [deploy/docker-compose.yml](deploy/docker-compose.yml). Mount các đường dẫn cần backup (chỉ đọc) vào đúng đường dẫn trong config; `network_mode: host` để uptime check và `pg_dump` tới được dịch vụ trên server.

### Font và locale khi chạy trên VPS

Màn hình `sentinel manage` có chữ tiếng Việt và vẽ khung, nên cần hai thứ, cả hai nằm ngoài Sentinel:

- **Font của terminal phía máy bạn** (nơi mở SSH) phải có chữ Việt. Font như `MesloLGS NF` thiếu nhiều chữ có dấu (ả, ấ, ầ, ố, ế, ỗ...), nên "lỗi", "cảnh báo" chỉ còn dấu. Dùng `JetBrains Mono` hoặc `Cascadia Mono`. Trong VS Code: `"terminal.integrated.fontFamily": "JetBrains Mono, MesloLGS NF, monospace"`, để font Nerd phía sau chỉ lo icon.
- **Locale UTF-8 trên server.** Nhiều image VPS để locale `POSIX`. Chạy `export LANG=C.UTF-8` (thêm vào `~/.profile`). Trong `tmux` thêm `-u`. `sentinel doctor` cảnh báo `locale` khi locale không phải UTF-8.

## Lưu ý

- Kho từ xa (S3, sftp, rclone) phải tạo bằng `sentinel backup init <job>` một lần. Kho là thư mục trên chính máy này thì tự tạo ở lần backup đầu, nếu thư mục chưa có hoặc đang trống.
- Nếu `pg_dump` thất bại thì restic không tạo snapshot và lần chạy được ghi là lỗi. Backup có snapshot nhưng không đọc được một số file cũng tính là lỗi.
- Job có nhiều repository thì mỗi repository được backup riêng.
- Khi repository đang bị khóa, daemon thử lại 3 lần, cách nhau 5 phút. Sentinel không bao giờ tự `unlock`.
- Daemon khởi động lại thì không gửi lại các cảnh báo đang mở, nhưng vẫn báo khi chúng được khắc phục.
- Lịch sử uptime giữ 30 ngày; lịch sử backup giữ vĩnh viễn.
- Backup theo lịch báo `unable to locate cache directory: neither $XDG_CACHE_HOME nor $HOME are defined`: systemd không đặt `HOME` cho service chạy root khi unit không có `User=`, nên restic không tìm được thư mục cache. Unit mới của repo đã có `Environment=HOME=/root`; unit cài từ bản cũ thì chạy lại `deploy/install.sh` hoặc thêm dòng đó vào `/etc/systemd/system/sentinel.service` rồi `systemctl daemon-reload && systemctl restart sentinel`. `sentinel doctor` chạy từ shell có `HOME` nên không thấy lỗi này; thử đúng môi trường của daemon bằng `systemd-run --wait --pipe -p EnvironmentFile=/etc/sentinel/sentinel.env sentinel backup run <job>`.
- MariaDB client ≥ 11.4 mặc định đòi TLS; server không có TLS thì đặt `tls: disabled` trong khối `mysql:`.

## Phát triển

```sh
make test         # golangci-lint + govulncheck + go vet + go test
make e2e          # Postgres + S3 bằng docker
make e2e-engines  # MariaDB, MySQL, MongoDB bằng docker
```
