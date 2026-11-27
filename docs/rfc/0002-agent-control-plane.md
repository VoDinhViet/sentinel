# RFC 0002: Agent và Control Plane

| | |
| --- | --- |
| Status | Draft |
| Author | VoDinhViet |
| Created | 2026-11-27 |
| Supersedes | [RFC 0001](0001-multi-server-monitoring.md) (mô hình pull) |

## 1. Tóm tắt

Mỗi server chạy một **agent** (daemon `sentinel run` hiện có) và nối **ra** một **control plane** (`sentinel-server`) qua mTLS. Agent báo trạng thái, nhận task từ hàng đợi, chạy task nằm trong allowlist local của nó rồi trả kết quả. Bạn điều khiển control plane bằng `sentinelctl` (CLI và TUI). Không có web UI. Agent không mở cổng nào.

RFC này thay mô hình pull của RFC 0001 (máy xem gọi API của từng daemon). Khi control plane chạy được, `internal/fleet`, `sentinel fleet`, FleetWatcher và API HTTP của daemon bị xoá (mục 15).

## 2. Vì sao đổi hướng

RFC 0001 cho phép **xem** nhiều server nhưng không giải quyết được:

1. **Thao tác từ xa** (chạy backup, restart service, cập nhật) mà không SSH vào từng máy.
2. **Agent sau NAT hoặc firewall**: pull cần mở cổng API trên mọi server, push (agent gọi ra) thì không.
3. **Một chỗ ghi lịch sử và audit**: ai làm gì, lúc nào, trên server nào.
4. **Một chỗ báo "server ngừng báo cáo"**, thay cho việc từng máy canh nhau.

### 2.1. Khác gì thiết kế đã xoá ngày 2026-11-23

Thiết kế push cũ bị xoá có chủ ý (commit `dda2b02`; bản lưu ở `/home/workspace/project-backup-pre-web-removal-2026-11-23.tar.gz`). Lần này khác ở chỗ:

| | Bản cũ | RFC này |
| --- | --- | --- |
| Giao diện | Web UI React | Chỉ CLI + TUI |
| Agent xác thực | Bearer token | mTLS, cert ngắn hạn, thu hồi được |
| Người dùng | users, roles, mật khẩu, session | **Một mật khẩu đăng nhập**, không username, không role |
| Việc server giao cho agent | Lệnh, đẩy config, restore | Bảng action cố định; agent kiểm tra lại bằng allowlist local |
| Đẩy config, restore từ xa | Có | Không |
| Thư viện | huma, sqlc, goose | Chỉ stdlib và các module đã có |

Lấy lại từ bản cũ các mẫu đã chạy tốt: hàng đợi lệnh long-poll, vòng poll có backoff, watchdog, audit append-only.

## 3. Mục tiêu và ngoài phạm vi

Quy mô: 5 đến 50 server Linux.

Mục tiêu:

1. Một nơi cho biết server nào online, lỗi, backup nào quá hạn.
2. Giao task cố định cho agent: backup, service, container, cập nhật OS, cập nhật agent.
3. Có lịch (cron) và cửa sổ bảo trì.
4. Lịch sử và báo cáo, audit không sửa được âm thầm.
5. Giữ nguyên: không AI, ưu tiên stdlib, daemon vẫn tự backup và tự cảnh báo khi mất control plane.

Ngoài phạm vi: shell tùy ý, đẩy config, restore từ xa, reboot, control plane HA, web UI, nhiều tài khoản và phân quyền, AI/LLM.

## 4. Kiến trúc

```text
 máy của bạn                    máy control plane                       mỗi server
┌────────────┐  HTTPS  ┌───────────────────────────┐   mTLS   ┌──────────────────┐
│ sentinelctl├────────►│ sentinel-server           │◄─────────┤ sentinel run     │
│ (CLI, TUI) │ mật khẩu│  ctl_listen 127.0.0.1   │ agent gọi│  transport       │
└────────────┘ +session│  listen :9320             │   ra     │  executor+policy │
                       │  SQLite, CA, audit        │          │  backup, host    │
                       └───────────────────────────┘          └──────────────────┘
```

Ba Go module trong một repo, nối bằng `go.work`:

- `pkg/` (`sentinel/pkg`): phần dùng chung, không biết gì về agent hay server.
- `agent/` (`sentinel/agent`): code hiện tại, binary `sentinel`.
- `server/` (`sentinel/server`): control plane, binary `sentinel-server` và `sentinelctl`.

`agent` và `server` chỉ gặp nhau qua `pkg/agentapi`. Mỗi bên không import được `internal/` của bên kia.

## 5. Cấu trúc repo

```text
project-backup/
├── go.work                      use ./pkg ./agent ./server
├── deploy/agent/  deploy/server/
├── pkg/
│   ├── agentapi/   hợp đồng agent ↔ server: Report, HostStatus, Task, TaskResult, bảng Actions + validate
│   ├── pki/        CA, key/CSR, ký cert, fingerprint, tls.Config
│   ├── alert/ notify/ termtext/   chuyển từ agent
│   ├── yamlenv/    DecodeStrict, ${VAR}, đọc và kiểm tra file .env
│   ├── ratelimit/  token bucket
│   └── release/    manifest bản phát hành, ký và kiểm tra ed25519
├── agent/
│   ├── cmd/sentinel/
│   └── internal/   app daemon backup engine restic store uptime schedule status doctor config configedit tui host
│                   + transport (client mTLS) + executor (backup, service, docker, update, selfupdate, policy)
└── server/
    ├── cmd/sentinel-server/   serve, init, enroll, passwd, audit verify
    ├── cmd/sentinelctl/       login, logout, passwd, server, alert, task, run, audit, schedule, report; không lệnh con thì mở TUI
    └── internal/   app config store auth audit enroll inventory task watchdog scheduler report httpapi ctlapi client tui
```

Hướng phụ thuộc, không vòng: `agent → pkg`, `server → pkg`, `pkg` không import ai. Trong server: `httpapi` và `cmd` → `app` → các package nghiệp vụ → `store`, `pkg/*`. `server` không import `agent` (trừ test end-to-end).

`status.Report` phụ thuộc `backup`, `config` và `store` của agent nên không đưa vào `pkg` được. `agentapi.Report` là kiểu riêng của hợp đồng; agent chuyển `status.Report` sang nó trước khi gửi.

## 6. Thuật ngữ

| Thuật ngữ | Nghĩa |
| --- | --- |
| agent | Một server đã enroll (trong Go và DB). CLI và TUI gọi là "server" |
| task | Một action từ xa nằm trong hàng đợi |
| action | Loại task, ví dụ `backup.start` |
| join token | Mã dùng một lần để agent đổi lấy cert |
| session | Phiên đăng nhập bằng mật khẩu |

`task` ở đây không trùng `daemon.Task` (việc định kỳ trong daemon): hai thứ nằm ở hai module khác nhau.

## 7. PKI và enroll

- **CA** do `sentinel-server init` tạo (ECDSA P-256, 10 năm), khoá ở `state_dir` mode 0600.
- **Cert server**: do CA ký, hạn 1 năm, SAN là các host/IP trong URL của control plane. Server gửi kèm CA trong chuỗi cert (cert server trước, CA sau) để agent đang join lấy được CA từ fingerprint.
- **Cert agent**: ECDSA P-256, hạn 90 ngày, tự gia hạn khi còn 30 ngày. Khoá của agent **không rời máy agent**, được sinh lại mỗi lần gia hạn.
- **Danh tính** lấy từ URI SAN `sentinel://agent/<tên>`. CSR chỉ chứa khoá công khai (không có tên): server tự đặt tên, và subject, SAN hay yêu cầu IsCA trong CSR đều bị bỏ qua. Cert chỉ có ExtKeyUsage ClientAuth, không bao giờ IsCA, serial ngẫu nhiên 128-bit.
- **Thu hồi**: bảng `certs`; control plane kiểm tra serial ở mọi request của agent, nên không cần phát CRL.
- **Nơi lưu** trên agent: `<thư mục store>/agent/{cert,key,ca}.pem`. Không dùng `/etc` vì `ProtectSystem=full` khoá ghi.

Luồng enroll:

1. `sentinelctl server add web01` (hoặc `sentinel-server enroll web01` trên máy control plane) tạo agent `pending` và một join token (24 giờ, dùng một lần, lưu SHA-256), in ra lệnh join.
2. Trên server: `sentinel agent join --url … --token … --ca-fingerprint sha256:…`. Agent sinh khoá và CSR, gọi `POST /v1/enroll/join` qua kết nối **ghim fingerprint của CA** (`pki.PinnedTLSConfig(fingerprint, host)`: CA trong chuỗi cert server phải có đúng fingerprint, cert server phải do CA đó ký, đúng host trong URL, còn hạn, dùng cho server), nhận cert và CA, kiểm tra lại câu trả lời (CA đúng fingerprint, cert do CA đó ký, đúng tên, đúng khoá vừa tạo) rồi mới ghi file, cert ghi sau cùng. Token có thể đưa qua `--token`, `--token-stdin` hoặc nhập trên terminal; `--token` hiện trong `ps` trong lúc lệnh chạy (token dùng một lần và sống 24 giờ, nên chấp nhận được, nhưng help nói rõ). Sau khi join, agent gọi `GET /v1/agent/whoami` bằng cert mới để chắc rằng nó dùng được.
3. Agent chuyển `active`. Từ đó mọi request dùng mTLS.

`pki.PinnedTLSConfig` là nơi duy nhất được dùng `InsecureSkipVerify`, kèm `VerifyConnection` tự kiểm tra chuỗi cert. Host được truyền vào vì client dial bằng IP không ghi tên vào `ConnectionState`, và kiểm tra với tên rỗng sẽ bỏ qua bước so host. `make layers` chặn mọi `InsecureSkipVerify` ngoài `pkg/pki`.

## 8. Giao thức agent

Mọi route agent dùng mTLS (TLS 1.3). Mỗi request cập nhật `last_seen`.

| Route | Nội dung |
| --- | --- |
| `POST /v1/enroll/join` | `{token, csr}` → `{name, cert, ca, not_after}`; không cần client cert; giới hạn tần suất theo IP, body tối đa 16 KiB |
| `GET /v1/agent/whoami` | → `{name, cert_not_after}`: control plane đọc tên từ cert; dùng để kiểm tra cert hoạt động |
| `POST /v1/agent/report` | `{version, sent_at, host, backups, checks, alerts}` → `{server_time, report_every_s}`; body tối đa 512 KiB, mỗi danh sách tối đa 200 phần tử, mọi chuỗi qua `termtext.Clean` và bị cắt; trường lạ được bỏ qua (agent mới hơn server vẫn báo được) |
| `POST /v1/agent/renew` | `{csr}` → `{cert, not_after}`; chỉ khi cert hiện tại còn dưới 45 ngày và agent chưa được cấp cert nào trong giờ qua (429 nếu có: chặn vòng lặp làm đầy bảng `certs` và audit); cert cũ vẫn dùng được cho đến khi agent đưa cert mới ra lần đầu, lúc đó các cert cũ hơn của nó bị thu hồi (bản sao khoá cũ hết tác dụng, và `server ls` hiện hạn của cert đang dùng chứ không phải của cert đã cấp mà agent chưa nhận) |
| `GET /v1/agent/tasks?wait=25s` | long-poll → `[{id, action, params, result_by}]`; mỗi lần poll giao tối đa một việc (`queued` chuyển sang `sent` trong một transaction), vì `result_by` tính từ lúc giao mà agent chạy từng việc một; việc đến sau hạn `result_by` thì agent không chạy |
| `POST /v1/agent/tasks/{id}/result` | `{state, started_at, finished_at, output, error}` → 204; chỉ nhận một lần, chỉ từ agent được giao việc (agent khác nhận 404), thời điểm bắt đầu bị kẹp giữa `sent_at` và bây giờ |
| `POST /v1/agent/tasks/{id}/log` | một đoạn log (tối đa 16 KiB; nhiều dòng thì mỗi dòng một bản ghi), mỗi task tối đa 256 KiB và 2000 dòng |

Phía `sentinelctl`: `GET|POST /v1/ctl/tasks`, `GET|DELETE /v1/ctl/tasks/{id}` (DELETE huỷ việc còn `queued`) và `GET /v1/ctl/tasks/{id}/events` (SSE: `state`, `log`, `done`; `Last-Event-ID` nối lại từ dòng log đã nhận; tối đa 10 stream cùng lúc, mỗi phút kiểm tra lại phiên nên đổi mật khẩu cũng cắt stream, và tắt server thì stream và long-poll kết thúc ngay). Server cũng chạy `restic.Redact` (nay là `pkg/redact`) trên output, lỗi và log của task: agent là lớp thứ nhất, server là lớp thứ hai.

- `backups` là lần chạy mới nhất của từng job vào từng repository. Lịch sử các lần chạy (`backup_runs` với cursor) và `host_samples` thuộc giai đoạn 14, vì khi đó agent gửi lại từ cursor 0 nên không mất gì.
- `allow` trong report là các action mà `control_plane.allow` của agent đang cho phép; `sentinelctl server show` in ra, để biết trước việc nào sẽ bị `refused`. Control plane không chặn trước: agent mới là nơi quyết định.
- `report_every_s` là nhịp server muốn (agent giữ trong 15 giây đến 10 phút, lệch ±10%); `server_time` để agent báo khi đồng hồ lệch quá 2 phút.
- Mỗi agent bị giới hạn tần suất gọi (1 lần/giây, burst 10) sau khi cert được kiểm tra. Chuỗi trong report là một dòng (`termtext.Line`: xuống dòng và tab thành dấu cách) và bị cắt; danh sách phần máy cũng giới hạn 200 phần tử.
- Agent gia hạn khi cert còn dưới 30 ngày, thử lại sau một giờ nếu hỏng; khoá mới được ghi vào `key.next.pem` trước khi hỏi control plane, nên thư mục không ghi được thì không cấp cert vô ích (`sentinel doctor` cũng kiểm tra). Sau khi bị từ chối (401) agent đọc lại file danh tính trước lần thử kế, nên `agent join --force` có hiệu lực không cần khởi động lại; `agent join` xoá `*.next.pem` còn sót.
- Report trượt thì thử lại sau 20 giây chứ không đợi cả nhịp; không dựng được report (database bận) thì agent chỉ gọi whoami để vẫn hiện là còn sống. Khoá mới được sinh mỗi lần; khoá và cert mới ghi thành `key.next.pem`, `cert.next.pem` rồi `rename` đè, và `LoadIdentity` chọn cặp khớp nhau, nên mất điện giữa chừng không khoá agent ra ngoài.
- Một lần poll mới của cùng agent huỷ lần poll trước.
- Agent report mỗi 60 giây (lệch ±10%), poll liên tục. Gặp 401 thì ghi log lỗi rồi im một giờ mới hỏi lại (không lặp nóng, nhưng cũng không bỏ hẳn: một agent được đặt lại sẽ tự về).

## 9. Đăng nhập

Chỉ **một mật khẩu đăng nhập**. Không username, role, API token, cert hay MFA. Ai đăng nhập được thì có toàn quyền; giới hạn quyền nằm ở allowlist local của từng agent (mục 10).

- Băm bằng PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`), 600.000 vòng, salt 16 byte, lưu `pbkdf2-sha256$<vòng>$<salt>$<hash>`. So sánh bằng `subtle.ConstantTimeCompare`. Tối thiểu 12 ký tự.
- `sentinelctl login` hỏi mật khẩu (hoặc `--password-stdin`), gọi `POST /v1/ctl/login`, nhận session token 256-bit. Session hết hạn sau 12 giờ, hoặc 1 giờ không dùng. Server chỉ lưu SHA-256 của token.
- Client giữ session ở `~/.config/sentinel/session` (0600); từ chối file có quyền rộng hơn. `ctl.yaml` cạnh nó chỉ có `url` và `ca_fingerprint`: `sentinelctl` ghim fingerprint của CA (cùng `pki.PinnedTLSConfig` như lúc join) ở mọi kết nối thay vì giữ file CA, và không theo redirect.
- Chống dò mật khẩu: một IP sai 5 lần liên tiếp bị chặn 15 phút (kể cả khi sau đó nhập đúng). Khi cả server đã thấy 30 lần sai trong một giờ, mỗi IP chỉ được sai 1 lần trước khi bị chặn. Không có khoá toàn cục: một IP chưa sai lần nào vẫn đăng nhập được, nên kẻ tấn công không khoá được chủ mật khẩu thật. Lần sai bị quên sau một giờ; đăng nhập đúng xoá bộ đếm của IP đó. Đổi mật khẩu cũng đi qua cùng bộ đếm, nên không dùng nó để dò mật khẩu cũ được. Mỗi lượt thử được đếm **trước** khi băm, trong cùng transaction với việc kiểm tra bị chặn, nên gửi 1000 request cùng lúc từ một IP cũng chỉ được 5 lượt. Tối đa 2 lần băm chạy song song. Còn lại: nhiều IP khác nhau (IPv6 /64) vẫn tốn CPU băm và thêm dòng audit `login.fail`; nhật ký không bao giờ xoá, nên đây là giới hạn đã biết.
- Audit ghi mọi lần đăng nhập đúng, sai và lần bị chặn đầu tiên; không ghi các request bị chặn tiếp theo và các lần join sai token (chỉ `slog`), vì nhật ký không bao giờ xoá và ai cũng gửi được request sai.
- `sentinelctl passwd` đổi mật khẩu và thu hồi mọi session. Quên mật khẩu thì chạy `sentinel-server passwd` bằng root trên máy server. Qua SSH tunnel mọi client cùng địa chỉ 127.0.0.1, nên ai trên máy đó cũng dùng hết được lượt thử của chủ mật khẩu; `sentinel-server unblock` (và `passwd`) xoá bộ đếm và gỡ chặn ngay.
- Audit ghi người thao tác là `ctl` cùng IP và 8 ký tự đầu của hash session. Request tới `ctl_listen` có token không phải session thì trả 401 mà không ghi audit (ai tới được cổng cũng gửi được, mà nhật ký không bao giờ xoá). Đổi mật khẩu với mật khẩu cũ sai trả 403, không phải 401, để client phân biệt với "phiên đã hết". IP của client được chuẩn hoá: bỏ cổng, IPv6 gộp theo /64, không tin header proxy. Mọi route của `sentinelctl` có giới hạn tần suất theo IP (token bucket, `pkg/ratelimit`) và body tối đa 4 KiB.
- Chứng chỉ của server (hạn 1 năm) do chính control plane cấp lại nếu còn dưới 30 ngày hoặc `hosts` có tên mới, kiểm tra khi `serve` khởi động và cứ 10 phút một lần khi đang chạy (kết nối mới dùng cert mới, không cần restart); CA không đổi nên agent không phải làm gì. Cert luôn có thêm `localhost`, `127.0.0.1`, `::1` cho `sentinelctl` qua SSH tunnel.

`ctl_listen` mặc định `127.0.0.1:9321`: dùng trên máy control plane hoặc qua SSH tunnel; có thể gắn vào địa chỉ WireGuard/Tailscale.

## 10. Action và allowlist

Bảng action cố định trong `pkg/agentapi`; tham số được decode bằng `DisallowUnknownFields` và validate cả ở server lẫn agent.

| Action | Tham số | Ghi chú |
| --- | --- | --- |
| `backup.start` / `backup.stop` | `job` (+ `repository` tuỳ chọn cho `backup.start`) | `job` phải có trong config local; `backup.start` xong khi backup đã *bắt đầu* (kết quả là pid), còn kết quả backup đến ở report kế tiếp |
| `backup.snapshots` | `job`, `repository` tuỳ chọn | chỉ đọc; 50 snapshot mới nhất; bỏ `repository` thì dùng repository duy nhất của job |
| `doctor.run` | — | kết quả đã `restic.Redact`; có check lỗi thì task `failed` ("N of M checks failed") |
| `service.start\|stop\|restart` | `unit` | `^[A-Za-z0-9][A-Za-z0-9:_.@-]*\.(service\|timer)$`, khớp chính xác trong `allow.services`; không bao giờ `sentinel.service` (cả server lẫn agent từ chối). Trước hết `systemctl show --property=Id --value -- <unit>`: unit là bí danh của `sentinel.service` (hoặc systemd không trả lời) thì `refused`. Rồi chạy `systemctl --no-ask-password --no-pager <động từ> -- <unit>` (argv cố định, unit đứng sau `--`, môi trường tối thiểu, tìm `systemctl` ở `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`), rồi `systemctl is-active -- <unit>`: unit ở trạng thái `failed` thì task `failed`, còn lại báo trạng thái trong output |
| `container.restart` | `container`, `timeout_s` tuỳ chọn (1 đến 90) | `^[A-Za-z0-9][A-Za-z0-9_.-]*$`, khớp chính xác trong `allow.containers`; `GET /containers/{tên}/json` để biết tên đó đúng là container (Docker còn hiểu tên là tiền tố của id: `Name` phải là `/tên` hoặc `Id` phải bằng tên), rồi `POST /containers/{id đầy đủ}/restart[?t=N]` qua unix socket `allow.docker_socket`, không theo redirect, không proxy; 204 là thành công, 404 là không có container đó |
| `os.check_updates` | — | cần `allow.os_updates: check` hoặc `apply`. Chạy `apt-get -s -o Debug::NoLocking=1 upgrade` (mô phỏng, không đổi gì), báo số package có thể nâng cấp, số bản vá bảo mật (suite có `-security`), package bị giữ lại, 100 package đầu, tuổi của package lists và máy có đang chờ khởi động lại không. Chỉ Debian và Ubuntu (apt); không có `dnf` |
| `os.apply_updates` | — | cần `allow.os_updates: apply`; `sentinelctl run` hỏi gõ lại tên server (hoặc `--yes`), không có terminal mà thiếu `--yes` thì từ chối. Không có tham số: không `security_only`, không tên package, không `dist-upgrade`, không reboot. Timeout 30 phút |
| `agent.update` | `version` (dạng `1.2.0`) | cần `allow.self_update` (khoá công khai và địa chỉ manifest); `sentinelctl run` hỏi gõ lại tên server (hoặc `--yes`); mục 12. Timeout 10 phút |

Không có action nào chạy shell, nhận path, tên package hay lệnh tự do. Không có reboot.

Allowlist nằm trong `config.yaml` của agent, **mặc định tắt hết**:

```yaml
control_plane:
  url: https://cp.example.vn:9320
  allow:
    backup: false              # backup.start|stop|snapshots
    doctor: false              # doctor.run
    services: []               # service.*: đúng các unit này
    containers: []             # container.restart: đúng các container này
    docker_socket: /var/run/docker.sock
    os_updates: off            # off | check | apply
    self_update: {}            # public_key + manifest_url để bật
```

Agent kiểm tra lại mọi task theo thứ tự: bảng action (`agentapi.ValidateTask`), allowlist này, giới hạn tần suất (burst 3, 1 lần/5 giây cho mỗi action), rồi áp timeout (2 phút cho `backup.start`, 1 phút cho `backup.stop`, 3 phút cho `backup.snapshots` và `doctor.run`). Không đạt thì trả `refused` kèm khoá cần bật (`control_plane.allow.backup`). Log gửi lên được cắt qua `restic.Redact` (tối đa 200 dòng mỗi task, gom lô mỗi giây), và mỗi task được ghi log local, thứ mà control plane không xoá được. Allowlist đọc lúc daemon khởi động: đổi thì phải khởi động lại.

Việc ở server: `queued` quá 10 phút thì `expired`; `sent` mà quá `result_by` (hạn của action + 30 giây) cũng `expired`; mỗi agent tối đa 20 việc đang mở; việc đã xong giữ 30 ngày. Mỗi bước (`task.create|send|result|cancel|expire`) ghi audit. Agent gửi kết quả lại với backoff cho đến `result_by`; gặp 404 hoặc 409 thì bỏ, gặp 401 thì im một giờ.

`os.apply_updates` chạy qua `systemd-run --wait --collect --unit=sentinel-os-update --property=StandardOutput=append:<state>/os-update.log --property=StandardError=append:<state>/os-update.log -- /usr/bin/apt-get -y … upgrade` (argv cố định), vì `ProtectSystem=full` chặn daemon ghi `/usr`. Đây là lối ra khỏi sandbox có chủ ý, chỉ mở khi `os_updates: apply`. Chính đơn vị `sentinel-os-update` là khoá chống hai lần nâng cấp cùng lúc (lần thứ hai thấy tên đã dùng và thất bại); hết timeout của task (hoặc daemon dừng) thì agent thôi đợi nhưng **không** giết unit, vì dừng giữa chừng `dpkg` làm hỏng package. Unit ghi output ra một file chứ không qua pipe của agent (`--pipe` bị bỏ): pipe mà không còn ai đọc thì script của package gặp EPIPE giữa chừng. Agent đọc file đó để gửi log (mỗi dòng cắt ở 4 KiB, `\r` cũng ngắt dòng, luôn đọc hết); file nằm cạnh store, quyền 0600, bị ghi đè ở lần nâng cấp sau; xem thêm `journalctl -u sentinel-os-update`. Nâng cấp không chạy `apt-get update` trước: nó nâng cấp theo package lists hiện có. Trong lúc nâng cấp (tối đa 30 phút) worker của agent chỉ có một việc đó, các việc khác chờ ở hàng đợi và có thể hết hạn sau 10 phút. `apt-get` được gọi với `-y`, `--force-confdef` và `--force-confold` (giữ file config đang có), `DEBIAN_FRONTEND=noninteractive`, và tìm ở `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin` chứ không theo `PATH`.

## 11. Schema

SQLite (`modernc.org/sqlite`), migration nhúng bằng `embed` ở `server/internal/store/migrations/NNNN_<tên>.sql`, chỉ nối thêm; có test chặn sửa hay xoá file đã có.

| Migration | Bảng |
| --- | --- |
| 1, 2 | `agents`, `join_tokens`, `certs`, `login` (một dòng; migration 2 đổi tên từ `admin`: `password_hash`, `changed_at`), `sessions`, `login_failures`, `audit_log` (trigger chặn UPDATE/DELETE, chuỗi hash), `alerts` |
| 3 | `agent_reports` (báo cáo mới nhất của mỗi agent) |
| 4 | `tasks` (queued/sent/succeeded/failed/refused/canceled/expired), `task_logs` |
| 5 | `schedules`, `maintenance` |
| 6 | `host_samples` (một mẫu của máy mỗi agent ít nhất 5 phút một lần, giữ 30 ngày), `backup_runs` (mỗi lần backup mà agent báo xong, khoá `(agent, job, repository, finished_at)` nên báo lại cùng một lần thì bỏ qua; giữ 90 ngày; thời điểm xa tương lai hoặc quá 90 ngày bị bỏ) |

Audit: mỗi dòng có `hash = sha256(prev_hash ‖ nội dung chuẩn hoá)`; `sentinel-server audit verify` kiểm tra chuỗi. Mỗi entry cũng ghi `slog` để journald giữ bản thứ hai.

## 12. Tự cập nhật agent

- `pkg/release`: manifest `{version, os, arch, sha256, size, url}` ký ed25519 (chữ ký phủ cả một tiền tố riêng của bản phát hành, nên chữ ký làm cho việc khác bằng cùng khoá không bị nhận nhầm).
- **Task chỉ mang `version`** (`agent.update version=1.2.0`, cần xác nhận như `os.apply_updates`). Địa chỉ manifest và khoá công khai nằm trong config local của agent (`allow.self_update.manifest_url`, có `{version}`, `{os}`, `{arch}`; `allow.self_update.public_key`), nên control plane bị chiếm cũng không đưa được binary lạ hay trỏ agent sang chỗ khác. Chưa đặt đủ cả hai thì action bị từ chối.
- Trong daemon (có sandbox), `selfupdate.Stager`: tải manifest (https, không theo redirect, không qua proxy, giới hạn kích thước), kiểm tra chữ ký, khớp version, OS và arch của chính máy này, **không bao giờ cho hạ version** (`Newer`; agent đang chạy bản `dev` thì từ chối vì không so được), tải binary và kiểm tra kích thước và sha256, chạy thử `--version`, rồi ghi file `update/sentinel.new` và ghi chú `update/update.json` (cạnh file store).
- Đặt binary vào chỗ cần ghi `/usr/local/bin`, mà `ProtectSystem=full` chặn daemon, nên việc này làm trong unit tạm `sentinel-self-update` (`systemd-run --wait --collect --pipe --quiet`) chạy chính binary đó với lệnh ẩn `update-install` (argv cố định, không tham số từ task). Lệnh đó chỉ làm đúng điều ghi chú nói: file staged phải có đúng sha256 trong ghi chú, giữ binary cũ thành `sentinel.prev` (hard link), chép binary mới ra `sentinel.new` rồi `rename`. Đây là lối ra khỏi sandbox có chủ ý, chỉ mở khi `self_update` được đặt (`sentinel doctor` cảnh báo nếu thiếu `systemd-run`).
- Helper chạy bằng root không tin gì daemon ghi: manifest ký được giữ cạnh ghi chú (`update/manifest.json`) và `update-install` kiểm lại chữ ký bằng khoá công khai trong config mà chính nó đọc, so sha256 và version với ghi chú, băm các byte ngay lúc chép ra `sentinel.new` (không băm file rồi mới chép), từ chối thư mục `update/` mà người khác ghi được, xoá `sentinel.new` còn sót của lần cài bị kill, và ghi ghi chú `installed` *trước* khi `rename`. Trong lúc một bản đang thử việc, `agent.update` mới bị từ chối (không để mất `sentinel.prev` thật).
- **Người gác:** trước khi cài (và sau khi `Check` đã đạt: thư mục `update/` riêng tư, ghi chú là `staged` còn mới, chữ ký tốt), `update-install` chụp DB rồi khởi động dịch vụ tạm `sentinel-self-update-guard` (`systemd-run`, không timer) chạy `sentinel.prev update-guard`. Người gác kiểm mỗi 30 giây: ghi chú không còn `installed` (bản mới đã báo cáo) thì thoát; còn thì xem tiến trình `sentinel.service` (`MainPID` và thời điểm tiến trình bắt đầu trong `/proc`): nếu còn là daemon cũ đang đợi backup xong (bắt đầu trước lúc cài) thì đợi (tối đa 4 giờ), vì bản mới chưa chạy và chưa được thử việc; từ lúc không còn daemon cũ, bản mới có `TrialWindow` + 1 phút, hết thì người gác đặt `sentinel.prev` về chỗ cũ và `systemctl restart sentinel.service`. Nhờ vậy bản mới hỏng ngay từ `readConfig`, `openStore` hay bị treo vẫn bị gỡ, còn bản mới tốt không bị gỡ oan chỉ vì backup dài. Cài lỗi sau khi đã khởi động người gác thì `systemctl stop` người gác. Hai lần cập nhật liên tiếp: lần sau bị từ chối vì còn bản đang thử việc. Giới hạn: dịch vụ tạm không sống qua một lần reboot; nếu máy reboot khi bản mới còn đang thử việc thì chỉ còn đường tự quay về trong binary mới (`BeginTrial`, 3 lần start).
- **Schema của store:** binary cũ từ chối DB có `user_version` cao hơn nó biết, nên trước khi cài `update-install` chụp DB (`VACUUM INTO update/store.prev.db`, quyền 0600; không chụp được thì không cài). Khi quay về (`update-rollback` hoặc `update-guard`), sau khi kiểm việc quay về làm được (có `sentinel.prev` và một bản đang thử việc), nếu schema hiện tại cao hơn schema của bản chụp (hoặc không đọc được) thì DB hiện tại được đổi tên thành `state.db.rolled-back` và bản chụp được `rename` vào chỗ DB (không cần thêm dung lượng; `-wal` và `-shm` cũ bị xoá; daemon cũ còn mở file cũ thì không mất gì). Lịch sử ghi từ lúc chụp đến lúc quay về nằm trong `state.db.rolled-back`, không còn trong DB đang dùng. Bản chụp không khôi phục được thì ghi log và vẫn quay lại binary cũ. Bản chụp bị xoá khi bản mới báo cáo được; bản chụp luôn phải là file thường, thuộc người chạy helper.
- Gửi kết quả xong thì daemon thoát với mã lỗi (`Restart=on-failure`, `RestartSec=10s`), systemd chạy lại bản mới.
- **Thử việc:** bản mới có 5 phút để báo cáo thành công với control plane (`selfupdate.Confirm`), nếu không, hoặc nếu bị khởi động quá 3 lần trong lúc thử, daemon chạy `update-rollback` (đặt `sentinel.prev` về chỗ cũ) rồi tự khởi động lại; lý do ghi trong ghi chú và log. Giới hạn: control plane không liên lạc được trong 5 phút sau một lần cập nhật cũng kích hoạt quay về bản cũ. Khi daemon dừng để chạy bản mới, các backup đang chạy được đợi xong (`ShutdownWait`), nên có thể nhiều giờ server không báo cáo và bản mới chưa bắt đầu thử việc. Manifest không có version tối thiểu hay hạn dùng: control plane bị chiếm vẫn có thể đẩy một bản cũ hơn nhưng đã ký mà vẫn mới hơn bản đang chạy; chỉ có một khoá, chưa có xoay khoá.
- Chưa từng chạy thật ở môi trường phát triển (cần root và systemd); các bước được kiểm bằng test với hàm giả và với file trong thư mục tạm.
- `sentinelctl release keygen --out release.key` tạo cặp khoá (file khoá 0600, không ghi đè) và in khoá công khai để đưa vào config; `sentinelctl release sign --key … --binary … --version … --url …` ký manifest. `make dist` ký manifest cho từng kiến trúc khi có `SENTINEL_RELEASE_KEY` và `SENTINEL_RELEASE_URL` (thư mục có file binary), đồng thời xuất binary trần `sentinel_linux_<arch>`.

## 13. Cảnh báo

Control plane dùng lại `alert.Manager` và `notify` (chuyển sang `pkg`; `alert.Manager.Store` thành interface).

- `agent:<tên>` (Critical): `last_seen` cũ hơn `offline_after` (mặc định 3 phút, tối thiểu 3 phút = ba nhịp báo cáo, để một báo cáo trượt không đủ gây báo giả); có thời gian ân hạn sau khi control plane khởi động, và 30 phút sau khi join cho agent chưa gửi báo cáo nào (chưa kịp sửa config và khởi động lại daemon).
- `agent-cert:<tên>` (Warning): cert còn dưới 14 ngày.
- Cửa sổ bảo trì (`sentinelctl maintenance add <server>|--all --for 2h [--from …] [--reason …]`, tối đa 30 ngày, một lần, không lặp) làm `agent:` không được mở mới và đặt trạng thái `MAINTENANCE` cho server đang ở trong cửa sổ (chỉ ONLINE hoặc OFFLINE được đổi; PENDING và REVOKED giữ nguyên). Alert đã mở từ trước vẫn giữ đến khi server báo lại, và lúc đó vẫn gửi "is back". Alert chứng chỉ và alert agent tự gửi không bị ảnh hưởng. Không đọc được bảng cửa sổ thì coi như không có cửa sổ (báo thừa còn hơn im lặng khi server chết).
- Lịch (`sentinelctl schedule add <server> "<cron>" <action> [k=v…]`): 5 trường (phút giờ ngày tháng thứ; `*`, số, `a-b`, `a,b`, `/bước`, tên tháng và thứ bằng tiếng Anh, 0 và 7 đều là Chủ nhật; ngày-trong-tháng và thứ cùng được chỉ định thì khớp một trong hai như cron) hoặc `@hourly|@daily|@weekly|@monthly|@yearly`, có thể mở đầu bằng `CRON_TZ=Asia/Ho_Chi_Minh ` (mặc định múi giờ của máy control plane). Mỗi 20 giây `scheduler` xếp việc cho mọi lịch đến hạn qua đúng `task.Create` (lần chạy được giữ chỗ trước bằng `UPDATE … last_run_at` rồi mới xếp việc, nên dừng giữa chừng thì mất lần chạy chứ không nhân đôi; lịch chỉ xếp việc mới khi việc lần trước đã xong, và mỗi server chỉ có tối đa 10 lịch, để hàng đợi 20 việc còn chỗ cho người; server chưa join hay đã thu hồi thì bị bỏ qua, không ghi gì; giờ tính theo đồng hồ treo tường của múi giờ nên giờ không tồn tại khi đồng hồ nhảy tới bị bỏ qua và giờ lặp khi đồng hồ lùi chỉ chạy một lần) (cùng bảng action, cùng giới hạn 20 việc mở, cùng allowlist ở agent); việc mang `created_by = schedule:<số>`. Lần chạy lỡ quá 10 phút (control plane tắt, hay agent đầy hàng đợi) bị bỏ chứ không chạy muộn, và nhiều lần lỡ chỉ tính một. Không xếp được thì lý do ghi vào lịch (`last_error`) và audit `schedule.run`, lần sau thử lại theo giờ. Tối đa 10 lịch mỗi server và 200 lịch; một lần chạy xếp việc thành công thì chỉ có audit `task.create` (người tạo `schedule:<số>`), còn lần không xếp được mới có `schedule.run` kèm lý do. Cửa sổ bảo trì phải ghi rõ một server hoặc `all` (API từ chối tên rỗng), bắt đầu tối đa một năm tới. Action cần xác nhận (`os.apply_updates`) khi tạo lịch cũng hỏi gõ lại tên server, như `run`.
- Control plane **không** chuyển tiếp alert mà agent đã tự gửi; nó chỉ báo điều agent không tự báo được.
- Đặt `heartbeat.url` cho chính control plane để biết khi nó chết.
- `server.yaml` có `notifiers`, `cooldown` (mặc định 1 giờ) và `heartbeat`, cùng kiểu với config của agent. Server không dùng thư viện validator của agent nên `notify.Settings.Validate` và `HeartbeatSettings.Validate` (trong `pkg/notify`) kiểm tra các trường bắt buộc và URL https. Alert lưu trong bảng `alerts`, sống qua lần khởi động lại.

## 14. CLI và TUI

- Lịch sử và báo cáo: mỗi report của agent được `report.Record` ghi thêm vào lịch sử (đồng hồ tính là của control plane, số liệu bị kẹp vào 0 đến 100%; ghi hỏng không làm hỏng report). `sentinelctl report host <server> [--hours N]` (CPU, RAM, ổ đầy nhất và load: trung bình, cao nhất, biểu đồ khối; tối đa 300 cột, mỗi cột là trung bình của một bước ít nhất 5 phút) và `sentinelctl report backups [--server X] [--days N]` (mỗi job: số lần, số lần lỗi, lần cuối, thành công cuối, kết quả cuối, và 20 lần lỗi mới nhất); cả hai có `-o json`. Phần Máy trong TUI hiện biểu đồ 24 giờ. `digest_at: "08:00"` trong `server.yaml` (giờ của máy control plane, rỗng là tắt) gửi mỗi ngày một tin cho các notifier: số server theo trạng thái, các server không ONLINE, backup lỗi 24 giờ qua và alert đang mở (Warning nếu có gì không ổn, Info nếu không).

- `sentinel-server`: `init`, `serve`, `enroll`, `passwd`, `audit verify` (chạy bằng root trên máy control plane).
- `sentinel agent join|status` trên server.
- `sentinelctl`: `login`, `logout`, `passwd`, `server ls|add|revoke|show`, `alert`, `task`, `run <server> <action> k=v [--wait|--follow]`, `audit`, `schedule`, `maintenance`, `report`, `release`; có `-o json`. Không lệnh con thì mở TUI (chỉ khi cả stdin và stdout là terminal; ngược lại in trợ giúp).
- `--follow` và màn hình task dùng SSE (`GET /v1/ctl/tasks/{id}/events`).
- TUI (`server/internal/tui`, bubbletea và lipgloss, cùng phiên bản với agent) đọc qua `client`, không đọc DB: hai tab Server và Alert, và màn hình một server với bốn phần (Máy, Backup, Uptime, Alert). Hỏi lại control plane mỗi 5 giây, không bao giờ chồng hai lần hỏi lên nhau; phiên hết hạn thì thoát và báo chạy `sentinelctl login`. Nó không dùng chung code với `agent/internal/tui` (server không import agent); chỉ bảng màu và cách vẽ bảng giống nhau.
- Mô tả lệnh và màn hình bằng tiếng Việt.

## 15. Xoá mô hình pull

Làm sau khi control plane thay được `fleet status`, `fleet alerts` và FleetWatcher, mỗi bước một commit, `make test` xanh sau mỗi bước:

1. Bỏ tab Fleet khỏi `sentinel manage`.
2. Xoá `cmd/sentinel/fleet*.go`.
3. Xoá `daemon/fleet.go`, `fleetTask`, `fleetFinding` và các key `fleet:`; thêm migration dọn alert `fleet:%` và `fleet-file` trong store của agent, để không còn alert mở vĩnh viễn.
4. Xoá `internal/fleet`.
5. Xoá `internal/api`, `startAPI` và `apiFinding`. Sau bước này daemon không nghe cổng nào, kể cả `/healthz`; offline alert và `heartbeat.url` thay cho nó.
6. `config.API` và `config.Fleet` giữ thêm một release dưới dạng deprecated: vẫn decode, bị bỏ qua, có cảnh báo khi start và trong `doctor`. Release sau xoá hẳn.

## 16. Mô hình đe doạ

| Rủi ro | Cách giảm |
| --- | --- |
| Lộ cert và khoá của một agent | Chỉ giả được agent đó. Khoá 0600, hạn 90 ngày, thu hồi có hiệu lực ngay |
| Lộ join token | Dùng một lần, 24 giờ, gắn một tên, lưu hash |
| Control plane hoặc CA bị chiếm | Chỉ xếp hàng được action trong bảng cố định; agent kiểm tra lại với allowlist local (mặc định tắt); không shell, path, restore, config push; rate limit; log local; khoá cập nhật nằm trên agent |
| Dò mật khẩu đăng nhập | Loopback mặc định, TLS 1.3, PBKDF2, chặn theo IP, trần toàn server, session ngắn hạn lưu hash, audit mọi lần đăng nhập |
| Sửa audit | Trigger append-only, chuỗi hash, bản sao journald. Root trên máy control plane vẫn sửa được DB: rủi ro còn lại, ghi rõ |
| Text độc trong report hoặc log | `termtext.Clean`, `restic.Redact`, `MaxBytesReader`, giới hạn độ dài |
| Tiêm tham số | Regex, khớp chính xác allowlist, argv slice kèm `--`, `url.PathEscape` cho Docker |
| Secret lọt qua danh sách tiến trình | Chỉ thu tên tiến trình, không bao giờ cmdline, env hay user |
| Mất control plane | Agent vẫn backup và tự cảnh báo; backup `state_dir` (gồm khoá CA) bằng chính Sentinel |

## 17. Lộ trình

Mỗi giai đoạn nhỏ, có test riêng, `make test` xanh ở mọi module.

| # | Nội dung |
| --- | --- |
| 0 | Tài liệu: RFC này, CLAUDE.md, rules |
| 1 | Tái cấu trúc repo thành `agent/` + `go.work` (cơ học) |
| 2 | Module `pkg`: `termtext`, `notify`, `alert`, `yamlenv` |
| 3 | Collector (mạng, tiến trình) và `pkg/agentapi` |
| 4 | `pkg/pki` |
| 5 | Lõi server: config, store, auth, audit, enroll, `sentinel-server init\|enroll\|passwd` |
| 6 | `httpapi`, `serve`, join, `sentinelctl login\|logout\|passwd\|server\|audit`, `deploy/server/` (`deploy/` chưa đổi thành `deploy/agent/`: đường dẫn trong release và `install.sh` mà vận hành đang dùng, làm riêng khi kiểm tra được `make dist`) |
| 7 | Report, cảnh báo offline, gia hạn cert, `sentinelctl server show\|alert`, `control_plane` trong config agent, notifiers trong `server.yaml` (`host_samples` và `backup_runs` dời sang 14) |
| 8 | TUI của `sentinelctl`: danh sách server và alert, màn hình một server (chỉ đọc; thêm/thu hồi vẫn bằng lệnh; giai đoạn 9 thêm `b`/`s` và mục Việc) |
| 9 | Task engine và action `backup.*`, `doctor.run`; log qua SSE; `sentinelctl run\|task`; `control_plane.allow`; trong TUI phím `b`/`s` ở mục Backup (hỏi y/n, chặn trước nếu `allow` của server trong report không có action) và mục Việc |
| 10 | Xoá mô hình pull (mục 15): tab Fleet và `sentinel fleet`, FleetWatcher và alert `fleet:`, `internal/fleet`, `internal/api` (daemon không nghe cổng nào); `api:` và `fleet:` thành khối deprecated |
| 11 | Action systemd và Docker (`service.start\|stop\|restart`, `container.restart`; `allow.services`, `allow.containers`, `allow.docker_socket`) |
| 12 | Action cập nhật OS (`os.check_updates`, `os.apply_updates`; `allow.os_updates: off\|check\|apply`), chỉ apt |
| 13 | Lịch cron và cửa sổ bảo trì (`server/internal/scheduler`, migration 5 `schedules` và `maintenance`, `sentinelctl schedule add\|ls\|rm` và `maintenance add\|ls\|rm`; trạng thái MAINTENANCE; watchdog không báo offline trong cửa sổ). Parser cron viết bằng stdlib vì thêm `robfig/cron` vào module server cần bạn xác nhận |
| 14 | Lịch sử và báo cáo (`server/internal/report`, migration 6 `host_samples` và `backup_runs`; `sentinelctl report host\|backups`; biểu đồ CPU, RAM, ổ đĩa trong phần Máy của TUI; digest hằng ngày `digest_at`) |
| 15 | Tự cập nhật agent ký ed25519 (`pkg/release`, `agent.update`, `allow.self_update`, thử việc 5 phút và quay về `sentinel.prev`, `sentinelctl release keygen\|sign`, `make dist` ký manifest) |

## 18. Thư viện

Không thêm dependency mới cho dự án. Dùng `crypto/*`, `net/http`, `embed`, `testing/synctest` (stdlib), cùng `cenkalti/backoff/v5`, `gopsutil/v4` và (cho TUI) `bubbletea/v2`, `lipgloss/v2` đã có trong `go.mod`. Module `server` khai báo bubbletea và lipgloss cùng phiên bản với `agent`, nên cả repo vẫn chỉ dùng một bộ thư viện. Docker qua Engine API bằng `net/http` trên unix socket, không dùng Docker SDK; systemd qua `systemctl`, không dùng go-systemd.

## 19. Câu hỏi mở

1. Có chuyển tiếp alert local của agent qua control plane không? Đề xuất: không.
2. Một control plane không HA có đủ cho 5 đến 50 máy không? Đề xuất: có.
3. Xoay CA ở v1 làm thế nào? Đề xuất: join lại thủ công từng agent.

## 20. Kiểm tay trên máy thật

Các bước sau cần root và systemd nên chưa chạy được trong môi trường phát triển (không có Docker, không root). Chạy trên hai VM Debian hoặc Ubuntu trước khi dùng cho server thật, và ghi kết quả vào đây.

1. Control plane: `sentinel-server init`, `systemctl start sentinel-server`, `ss -ltnp` thấy hai cổng (agent và `127.0.0.1` cho sentinelctl).
2. Agent: `sentinelctl server add web01`, `sentinel agent join …` trên VM thứ hai, `systemctl start sentinel`. `ss -ltnp` trên VM này **không** thấy `sentinel` nghe cổng nào.
3. `sentinelctl server ls` thấy ONLINE. Rút mạng của VM thứ hai trên 3 phút: có alert `agent:web01`; nối lại: alert đóng.
4. Bật `allow.services: [nginx.service]`, `allow.containers`, `allow.os_updates: check`, rồi `sentinelctl run web01 service.restart unit=nginx.service --wait`, `container.restart`, `os.check_updates`. Từ chối đúng khi allowlist tắt.
5. **`systemd-run` từ trong unit đã hardening** (chưa kiểm): với `os_updates: apply`, `sentinelctl run web01 os.apply_updates --wait` phải tạo được unit `sentinel-os-update` và ghi `os-update.log`. Nếu `NoNewPrivileges`, `CapabilityBoundingSet` hay `RestrictAddressFamilies` chặn thì chỉnh `deploy/sentinel.service`, ghi lý do vào `.claude/rules/deploy-ops.md`.
6. Tự cập nhật: `sentinelctl release keygen`, đặt `public_key` và `manifest_url` trong config agent, build hai bản 1.0.0 và 1.1.0, `make dist` ký, rồi:
   - `agent.update version=1.1.0` thành công, daemon tự khởi động lại, `sentinel --version` là 1.1.0, `sentinel.prev` là 1.0.0 và timer `sentinel-self-update-guard` biến mất sau 7 phút;
   - bản 1.2.0 cố ý hỏng (thoát ngay khi đọc config): sau 7 phút `update-guard` đặt lại 1.1.0 và agent chạy lại;
   - bản có migration mới: sau rollback agent vẫn mở được `state.db` (nhờ `store.prev.db`);
   - manifest ký bằng khoá khác, hoặc `version` thấp hơn bản đang chạy, bị từ chối.

