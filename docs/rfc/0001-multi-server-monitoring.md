# RFC 0001: Multi-server monitoring (pull model)

| | |
| --- | --- |
| Status | Draft |
| Author | VoDinhViet |
| Created | 2026-11-27 |
| Related | [danh-gia-nang-cap.md](../danh-gia-nang-cap.md) |

## 1. Tóm tắt

Sentinel hiện chạy độc lập trên từng server: mỗi daemon tự backup, tự kiểm tra uptime và tự gửi alert. Khi có 5 đến 20 server, muốn biết "server nào đang có vấn đề" phải mở từng máy.

RFC này đề xuất một máy xem (laptop của admin hoặc một server bất kỳ) **kéo** trạng thái từ API của daemon trên từng server, rồi hiện chung trong lệnh `sentinel fleet` và trong một tab `Fleet`. Máy xem không cần cài Sentinel để backup: `sentinel fleet` chạy được khi không có `config.yaml`. Trên server đã có config thì `sentinel manage` cũng có tab `Fleet`. Không có server trung tâm, không database trung tâm, không web UI. Các daemon vẫn tự chạy backup và tự gửi alert, nên máy xem tắt thì không có gì mất.

## 2. Bối cảnh và mục tiêu

Phạm vi: 1 đến 20 server Linux, mỗi server chạy `sentinel run`.

Mục tiêu:

1. Một lệnh cho biết server nào lỗi, job nào quá hạn, backup nào đang chạy.
2. Biết khi một server không truy cập được hoặc daemon của nó đã dừng.
3. Xem snapshot, bắt đầu hoặc dừng backup của một server từ xa (giai đoạn sau).
4. Giữ nguyên hướng hiện tại: CLI và daemon, không AI, ưu tiên thư viện chuẩn.

Ngoài phạm vi: xem mục 11.

### 2.1. Vì sao không làm lại central server

Thiết kế đẩy (agent đẩy dữ liệu về server trung tâm có web UI) từng tồn tại và đã bị xoá có chủ ý ngày 2026-11-23 (commit `dda2b02`). Bản lưu đầy đủ trước khi xoá nằm ở `/home/workspace/project-backup-pre-web-removal-2026-11-23.tar.gz`; spec gốc là `docs/superpowers/specs/2026-10-06-sentinel-design.md`, mục "Giai đoạn 2". RFC này không đề xuất khôi phục thiết kế đó.

## 3. Vì sao kéo (pull) chứ không đẩy (push)

| | Pull (đề xuất) | Push (thiết kế cũ) |
| --- | --- | --- |
| Thành phần mới | Một client trong CLI | Server trung tâm, database, agent, web UI |
| Dùng lại | API của daemon đã có | Phải viết lại |
| Máy xem tắt | Không ảnh hưởng gì | Mất chỗ nhận dữ liệu |
| Biết server chết | Gọi không được là biết ngay | Phải đặt hạn "không thấy báo cáo" |
| Cần mở cổng | Mỗi server mở cổng API | Chỉ server trung tâm mở cổng |

Mặt hạn chế của pull: máy xem phải với tới được từng server (xem mục 5, cách nối mạng). Prometheus cũng chọn pull với các lý do tương tự: phát hiện target chết dễ hơn vì lần scrape thất bại là tín hiệu, và không bị dồn tải khi nhiều target cùng đẩy (xem [tài liệu tham khảo](#12-tài-liệu-tham-khảo)). Prometheus cũng thừa nhận có những tình huống khó dùng pull, như job ngắn hạn; backup của Sentinel không phải trường hợp đó vì daemon sống liên tục.

## 4. Những gì đã có

- **Các route của daemon** ([internal/api/api.go:46-53](../../internal/api/api.go#L46-L53)): `GET /healthz` (không cần token), `/api/v1/status`, `/api/v1/alerts`, `/api/v1/jobs`, `/api/v1/jobs/{job}/snapshots`, `POST /api/v1/jobs/{job}/backups`, `DELETE /api/v1/jobs/{job}/backups/running`, `/api/v1/doctor`.
- **`status.Report`** ([internal/status/status.go:15](../../internal/status/status.go#L15)) đã có `server`, `generated_at`, `daemon_running`, `alerts`, `jobs`, `checks`, `running`. Đủ để dựng bảng tổng hợp mà không đổi API.
- **Bearer token** bắt buộc, tối thiểu 16 ký tự (`config.MinTokenLength`, [internal/config/types.go:124](../../internal/config/types.go#L124)).
- **Cảnh báo cấu hình rủi ro**: `sentinel doctor` cảnh báo khi API nghe trên địa chỉ máy khác với tới được vì token đi qua mạng không mã hoá ([internal/doctor/doctor.go:199-208](../../internal/doctor/doctor.go#L199-L208)).
- **Interface `api.Backend`** ([internal/api/api.go:21](../../internal/api/api.go#L21)): Status, JobRepositories, ListSnapshots, StartBackup, RequestStop, Doctor. `*app.Service` là một cài đặt của nó.
- **Heartbeat** (`heartbeat.url`, mặc định 5 phút): daemon gọi ra một dịch vụ ngoài (healthchecks.io, Uptime Kuma push) để dịch vụ đó báo khi daemon ngừng gọi.

## 5. Làm được ngay, chưa cần code

Ba việc dưới đây dùng được với bản hiện tại.

1. **Báo khi daemon dừng.** Đặt `heartbeat.url` trên từng server. Đây là cách hiện có để biết daemon chết; `sentinel doctor` đã nhắc nếu chưa đặt. Fleet (RFC này) không thay thế heartbeat mà bổ sung một góc nhìn thứ hai từ máy xem.
2. **Một server gọi `/healthz` của server khác** bằng uptime check có sẵn (`UptimeCheck`, [internal/config/types.go:282](../../internal/config/types.go#L282)):

   ```yaml
   uptime:
     - name: sentinel-web01
       type: http
       url: http://10.8.0.11:9311/healthz
   ```

   Lỗi sẽ thành alert qua các kênh thông báo như mọi uptime check khác. Cách này chỉ cho biết daemon trả lời hay không, không cho biết job nào lỗi.
3. **Truy cập API an toàn** khi chưa có TLS: nghe trên `127.0.0.1` và đi qua SSH tunnel, hoặc nghe trên địa chỉ WireGuard hoặc Tailscale (khi đó `doctor` vẫn cảnh báo vì không biết đó là mạng riêng):

   ```sh
   ssh -N -L 19311:127.0.0.1:9311 web01 &
   curl -H "Authorization: Bearer $SENTINEL_API_TOKEN" http://127.0.0.1:19311/api/v1/status
   ```

## 6. Còn thiếu gì

| # | Thiếu | Vì sao ảnh hưởng |
| --- | --- | --- |
| F1 | Chưa có client nào gọi nhiều server | Phải chạy `curl` từng máy |
| F2 | API không trả version của Sentinel | Không biết server nào chạy bản cũ, client không biết route nào có |
| F3 | Số liệu máy (CPU, RAM, ổ đĩa, systemd unit lỗi) của `host.State` ([internal/host/host.go:27](../../internal/host/host.go#L27)) chỉ hiện trong tab `Server` của TUI, API chưa trả | Tổng hợp nhiều server không thấy ổ đĩa đầy hay unit lỗi |
| F4 | Chỉ có một token và nó cho phép bắt đầu, dừng backup | Máy chỉ để xem không nên mang quyền ghi |
| F5 | API không có TLS | Phải nhờ tunnel hoặc reverse proxy; `doctor` luôn cảnh báo khi nghe ngoài loopback |
| F6 | `sentinel manage` bắt buộc có `config.yaml` và state DB của chính máy đó ([cmd/sentinel/manage.go](../../cmd/sentinel/manage.go)) | Laptop không chạy backup sẽ không mở được tab `Fleet` trong `manage`; cần một đường vào không cần config |
| F7 | Daemon không biết gì về các server khác | Không ai canh chừng server nào không truy cập được khi máy xem tắt |

## 7. Thiết kế đề xuất

### 7.1. Tên gọi

| Thành phần | Tên |
| --- | --- |
| Package | `internal/fleet` |
| Lệnh | `sentinel fleet status`, `sentinel fleet alerts` (đều có `--output json`); `sentinel fleet` không kèm lệnh con mở TUI chỉ có tab `Fleet` |
| Config của máy xem | `~/.config/sentinel/fleet.yaml`, khoá gốc `servers:` |
| Tab của TUI | `Fleet` (tab thứ 7 của `sentinel manage`, và tab duy nhất của `sentinel fleet`) |
| Canh chừng | khối `fleet:` trong `config.yaml` của daemon, task `fleet:<server>`, alert key `fleet:<server>` |
| Khoá config mới của daemon | `api.read_token`, `api.tls_cert`, `api.tls_key` |
| Route mới | `GET /api/v1/host`; thêm trường `version` vào `/api/v1/status` (không vào `/healthz`, vì route đó không cần token) |
| Kiểu | `fleet.Config`, `fleet.Server`, `fleet.Client`, `fleet.ServerStatus` |

`fleet.ServerStatus` là một lần đọc một server (`Report`, `Err`, `FetchedAt`). Không dùng tên `Snapshot` để khỏi nhầm với snapshot của restic.

### 7.2. Config của máy xem

```yaml
# ~/.config/sentinel/fleet.yaml
servers:
  - name: web01
    url: https://10.8.0.11:9311
    token: ${SENTINEL_FLEET_TOKEN_WEB01}   # token chỉ đọc (api.read_token)
    tls_ca: /home/admin/.config/sentinel/ca.pem
  - name: db01
    url: http://127.0.0.1:19312            # qua SSH tunnel
    token: ${SENTINEL_FLEET_TOKEN_DB01}
```

Token đọc từ biến môi trường hoặc từ file `fleet.env` nằm cạnh `fleet.yaml`, theo cách `config.yaml` đọc `sentinel.env` (`config.LoadEnvFile`, `config.CheckEnvFile`). Lệnh từ chối chạy nếu `fleet.env` có quyền rộng hơn chủ file. Server dùng cert tự ký thì đặt chính cert đó làm `tls_ca`; không có tuỳ chọn bỏ qua kiểm tra cert hay ghim fingerprint, vì cách đó buộc phải bật `InsecureSkipVerify`.

### 7.3. Package `internal/fleet`

- `fleet.Client`: client `net/http` cho một server, có timeout, đặt header `Authorization`, giải mã JSON vào `status.Report`. Các thao tác ở giai đoạn 5 (snapshot, bắt đầu và dừng backup) cũng là method của client này.
- `fleet.Fetch(ctx, servers, limit)`: gọi song song có giới hạn (mặc định 8), trả `[]ServerStatus` đúng thứ tự trong `fleet.yaml`, để bảng không nhảy dòng giữa các lần đọc. Mỗi server có kết quả riêng. Server không truy cập được cho `Err` khác nil và hiện là "không truy cập được"; nó không làm hỏng danh sách của server khác.
- Song song giới hạn bằng channel làm semaphore và `sync.WaitGroup`, không thêm `golang.org/x/sync/errgroup` (xem mục 7.8 và 12).

### 7.4. Lệnh và TUI

- `cmd/sentinel/fleet.go` là lệnh cobra mỏng, chỉ gọi `internal/fleet`. Mô tả lệnh và cờ bằng tiếng Việt, theo quy ước của dự án.
- `sentinel fleet status`: một dòng mỗi server (tên, daemon chạy hay không, số alert mở, job quá hạn, backup đang chạy, tuổi của dữ liệu). `sentinel fleet alerts`: mọi alert đang mở, có cột server.
- Tab `Fleet` trong `sentinel manage`, và `sentinel fleet` không kèm lệnh con mở TUI chỉ có tab này (trên terminal; không phải terminal thì in help). Enter trên một dòng mở một `screen` chỉ đọc (cơ chế ngăn xếp trong [internal/tui/screen.go](../../internal/tui/screen.go)), hiện `status.Report` của server đó.

### 7.5. Thay đổi API của daemon

- `GET /api/v1/host`: số liệu của `host.State` (mục F3). Handler chỉ gọi `internal/app`, theo quy ước của `internal/api`.
- Trường `version` trong `status.Report`, lấy từ `main.version` ([cmd/sentinel/main.go:21](../../cmd/sentinel/main.go#L21)). `sentinel status -o json` có cùng trường này. Không đưa vào `/healthz`: route đó mở, không cần token, nên không nên cho biết phiên bản.
- `api.read_token`: token thứ hai chỉ gọi được các route `GET`. `api.token` giữ toàn quyền. Với `read_token` rỗng thì chỉ có một token như hiện nay.
- `api.tls_cert` và `api.tls_key`: nếu có, daemon phục vụ HTTPS bằng `crypto/tls` của thư viện chuẩn với `MinVersion` là TLS 1.2. Khi có TLS, `doctor` không cảnh báo nữa. Cert và key được nạp lúc daemon khởi động, nên lỗi lộ ra ngay.

### 7.6. Tương thích giữa client và server khác version

Các thay đổi trên chỉ **thêm**: route mới, field mới, khoá config mới. Client bỏ qua field lạ, và khi nhận 404 ở `/api/v1/host` thì hiện "server chưa nâng cấp" thay vì báo lỗi. Theo hướng dẫn của Google (AIP-180) thêm field vào response là thay đổi tương thích; không đổi nghĩa hay xoá field của `/api/v1`.

### 7.7. Canh chừng trong daemon (giai đoạn 4)

Daemon của một server đọc thêm khối `fleet:` trong `config.yaml`, trỏ tới một `fleet.yaml`, rồi chạy mỗi server trong file đó như một task `fleet:<server>` cùng loại với task uptime (`Task`, [internal/daemon/daemon.go](../../internal/daemon/daemon.go)):

```yaml
fleet:
  config: /etc/sentinel/fleet.yaml
  every: 1m
  fail_after: 3        # số lần liên tiếp không truy cập được trước khi báo
  stale_after: 10m     # generated_at cũ hơn mức này thì coi là dữ liệu đứng yên
```

Task mở alert `fleet:<server>` khi:

- server không truy cập được `fail_after` lần liên tiếp;
- `daemon_running` là `false`;
- `generated_at` cũ hơn `stale_after`.

Hết lỗi thì đóng alert. Làm trong daemon thay vì một lệnh `fleet watch` riêng vì `alert.Manager` đã lo sẵn: lưu trạng thái vào SQLite nên restart không báo lặp, nhắc lại theo cooldown, gửi lại tin lỗi (`RetryPending`) và đóng alert của server đã bỏ khỏi file (`ResolveMissing`). Một lệnh riêng sẽ phải tự viết lại các phần đó và cần thêm một systemd unit.

Task **không** gửi lại các alert mà từng server đã tự gửi, để khỏi báo trùng; nó chỉ báo chuyện mà server kia không tự báo được. Kênh gửi là `notifiers:` của chính daemon.

### 7.8. Thư viện

RFC này **không thêm dependency nào**. Mọi phần dùng thứ đã có trong [go.mod](../../go.mod) hoặc thư viện chuẩn:

| Việc | Dùng |
| --- | --- |
| Client và TLS | `net/http`, `crypto/tls`, `crypto/x509` (thư viện chuẩn) |
| Gọi song song có giới hạn | channel và `sync.WaitGroup` (thư viện chuẩn) |
| Đọc `fleet.yaml` | `go.yaml.in/yaml/v3`; kiểm tra bằng code thường vì file chỉ có vài trường |
| Đọc YAML và mở rộng `${VAR}` | `decodeStrict` của `internal/config` ([load.go:165](../../internal/config/load.go#L165)) làm cả hai, nhận `target any`; đang không export, giai đoạn 2 export thành `config.DecodeStrict` |
| Lệnh và TUI | `cobra`, `bubbletea` và `lipgloss`, như hiện nay |
| Thử lại khi lỗi mạng (nếu cần) | `cenkalti/backoff/v5`, đã dùng trong `internal/notify` |

Nếu khi làm thật thấy cần thêm thư viện, ví dụ `x/sync/errgroup` khi logic huỷ hàng loạt phức tạp hơn dự kiến, thì dừng lại, ghi vào RFC này lý do và phiên bản, và chờ xác nhận trước khi chạy `go get`.

## 8. Refactor cần làm trước

Làm theo hướng branch by abstraction: mỗi bước giữ nguyên hành vi, build và test xanh, rồi mới thêm tính năng lên trên. Mỗi bước là một commit riêng.

1. `api.New(backend, token)` thành `api.New(backend, api.Options{...})`, để thêm version và token chỉ đọc mà không đổi chữ ký hàm nhiều lần (giai đoạn 1).
2. Export `decodeStrict` thành `config.DecodeStrict`, để `internal/fleet` dùng chung cách đọc YAML (giai đoạn 2).
3. Tách phần dựng dòng của các tab Backups, Uptime và Alerts trong `tabs()` ([internal/tui/tabs.go:24](../../internal/tui/tabs.go#L24)) thành hàm nhận `status.Report`, để màn hình một server của fleet dùng lại (giai đoạn 3).

Không cần đổi `tui.RunManage` sang nhận interface: TUI đi qua struct `manageActions` ([internal/tui/actions.go:18](../../internal/tui/actions.go#L18)), chỉ có `RunManage` và `newManageActions` chạm tới `*app.Service`. Tab `Fleet` thêm một field vào struct đó, theo mẫu tab Server. Cũng không cần chuyển `api.Backend` sang `internal/app`, vì `fleet.Client` không phải cài đặt của nó. Không dùng strangler fig: không có hệ thống cũ nào phải thay dần.

## 9. Rủi ro và cách giảm

| Rủi ro | Cách giảm |
| --- | --- |
| Lộ token (token đi qua mạng) | Mỗi server một token, đọc từ biến môi trường, không ghi vào log; dùng tunnel hoặc TLS (7.5); `fleet.env` bị từ chối nếu quyền rộng hơn chủ file |
| Máy xem bị chiếm thì điều khiển được mọi server | Máy xem dùng `read_token`; token ghi chỉ cấp khi tới giai đoạn 5 |
| Đồng hồ các máy lệch nhau làm `generated_at` sai | So `generated_at` với giờ máy xem có dung sai, và hiện tuổi dữ liệu thay vì giờ tuyệt đối |
| Endpoint chậm (snapshots, doctor tới 100 giây, `slowCallTimeout` ở [internal/api/handlers.go:13](../../internal/api/handlers.go#L13)) | `fleet status` chỉ gọi `/api/v1/status` (nhanh); snapshots và doctor chỉ gọi khi người dùng mở xem |
| Nhiều server làm nghẽn máy xem | Giới hạn số request song song, timeout mỗi server |
| Cổng API mở ra ngoài | Chỉ nghe trên mạng riêng hoặc loopback; firewall; `doctor` tiếp tục cảnh báo |
| Daemon cũ không có route mới | Mục 7.6 |

## 10. Lộ trình

Mỗi giai đoạn nhỏ, có test riêng và `make test` xanh.

| Giai đoạn | Nội dung |
| --- | --- |
| 0 | Tài liệu, không code: sửa RFC cho khớp code, README hướng dẫn chạy nhiều server bằng heartbeat, uptime check `/healthz` và SSH tunnel (mục 5) |
| 1 | API của daemon: `version`, `api.read_token`, `GET /api/v1/host` |
| 2 | `internal/fleet`, `sentinel fleet status`, `sentinel fleet alerts` |
| 3 | Tab `Fleet` trong `sentinel manage`, màn hình một server, `sentinel fleet` mở TUI riêng |
| 4 | HTTPS cho API (`api.tls_cert`, `api.tls_key`), canh chừng trong daemon (mục 7.7) |
| 5 | Thao tác từ xa trong TUI (xem snapshot, bắt đầu và dừng backup) với token có quyền ghi |

Giai đoạn 1 và 2 đủ để dùng được; từ giai đoạn 3 trở đi là tiện dụng thêm.

## 11. Ngoài phạm vi

- Lưu lịch sử hoặc time series (không có database trung tâm); xem xu hướng thì dùng công cụ khác.
- Web UI và server trung tâm.
- Đẩy config từ xa xuống server hoặc quản lý config tập trung.
- Restore và sửa config từ xa (API hiện cũng chưa có).
- Tính năng AI hay LLM.

## 12. Tài liệu tham khảo

Tra ngày 2026-11-27.

### 12.1 Pull hay push

- Prometheus, [Pull doesn't scale – or does it?](https://prometheus.io/blog/2016/07/23/pull-does-not-scale-or-does-it/): lý do chọn pull và cách pull mở rộng (mục 3).
- Prometheus, [When to use the Pushgateway](https://prometheus.io/docs/practices/pushing/): các trường hợp pull không hợp (mục 3).

### 12.2 Giám sát và cảnh báo

- Google SRE Book, chương 6, [Monitoring Distributed Systems](https://sre.google/sre-book/monitoring-distributed-systems/): black-box và white-box; `/healthz` là black-box, `status` là white-box (mục 5 và 7.7).
- Google SRE Book, chương 10, [Practical Alerting](https://sre.google/sre-book/practical-alerting/): báo theo triệu chứng, tránh báo trùng (mục 7.7).

### 12.3 Bảo mật API

- OWASP, [REST Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/REST_Security_Cheat_Sheet.html): chỉ dùng HTTPS cho API và token (mục 7.5 và 9).
- Go, [crypto/tls](https://pkg.go.dev/crypto/tls): `Config.MinVersion` (mục 7.5).

### 12.4 Tương thích API

- Google, [AIP-180 Backwards compatibility](https://google.aip.dev/180): thêm field là thay đổi tương thích (mục 7.6).
- Go Blog, [Keeping Your Modules Compatible](https://go.dev/blog/module-compatibility): tiến hoá mà không phá người dùng cũ (mục 7.6).

### 12.5 Cấu trúc và refactor

- Go, [Organizing a Go module](https://go.dev/doc/modules/layout): `internal/` phẳng, chia theo trách nhiệm (mục 7.1).
- Martin Fowler, [Branch By Abstraction](https://martinfowler.com/bliki/BranchByAbstraction.html): cách chuyển dần sang interface (mục 8).
- Martin Fowler, [Strangler Fig](https://martinfowler.com/bliki/StranglerFigApplication.html): đối chiếu, không dùng (mục 8).
- [`x/sync/errgroup`](https://pkg.go.dev/golang.org/x/sync/errgroup) có `SetLimit` cho việc gọi song song có giới hạn. Không dùng để khỏi thêm dependency; channel và `sync.WaitGroup` làm được (mục 7.3).
