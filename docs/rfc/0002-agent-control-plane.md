# RFC 0002: Agent và Control Plane

| | |
| --- | --- |
| Status | Draft |
| Author | VoDinhViet |
| Created | 2026-11-27 |
| Supersedes | [RFC 0001](0001-multi-server-monitoring.md) (mô hình pull) |

## 1. Tóm tắt

Mỗi server chạy một **agent** (daemon `sentinel run` hiện có) và nối **ra** một **control plane** (`sentinel-server`) qua mTLS. Agent báo trạng thái, nhận task từ hàng đợi, chạy task nằm trong allowlist local của nó rồi trả kết quả. Admin điều khiển control plane bằng `sentinelctl` (CLI và TUI). Không có web UI. Agent không mở cổng nào.

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
| Người dùng | users, roles, mật khẩu, session | **Một mật khẩu quản trị**, không username, không role |
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
 máy admin                    máy control plane                       mỗi server
┌────────────┐  HTTPS  ┌───────────────────────────┐   mTLS   ┌──────────────────┐
│ sentinelctl├────────►│ sentinel-server           │◄─────────┤ sentinel run     │
│ (CLI, TUI) │ mật khẩu│  admin_listen 127.0.0.1   │ agent gọi│  transport       │
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
    └── internal/   app config store auth audit enroll inventory task watchdog scheduler report httpapi adminapi client tui
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
| session | Phiên đăng nhập của admin |

`task` ở đây không trùng `daemon.Task` (việc định kỳ trong daemon): hai thứ nằm ở hai module khác nhau.

## 7. PKI và enroll

- **CA** do `sentinel-server init` tạo (ECDSA P-256, 10 năm), khoá ở `state_dir` mode 0600.
- **Cert agent**: ECDSA P-256, hạn 90 ngày, tự gia hạn khi còn 30 ngày. Khoá của agent **không rời máy agent**, được sinh lại mỗi lần gia hạn.
- **Danh tính** lấy từ URI SAN `sentinel://agent/<tên>`. Subject trong CSR bị bỏ qua. Cert chỉ có ExtKeyUsage ClientAuth, không bao giờ IsCA, serial ngẫu nhiên 128-bit.
- **Thu hồi**: bảng `certs`; control plane kiểm tra serial ở mọi request của agent, nên không cần phát CRL.
- **Nơi lưu** trên agent: `<thư mục store>/agent/{cert,key,ca}.pem`. Không dùng `/etc` vì `ProtectSystem=full` khoá ghi.

Luồng enroll:

1. `sentinel-server enroll web01` tạo agent `pending` và một join token (24 giờ, dùng một lần, lưu SHA-256), in ra lệnh join.
2. Trên server: `sentinel agent join --url … --token … --ca-fingerprint sha256:…`. Agent sinh khoá và CSR, gọi `POST /v1/enroll/join` qua kết nối **ghim fingerprint** của cert server (`pki.PinnedTLSConfig`), nhận cert và CA, ghi file.
3. Agent chuyển `active`. Từ đó mọi request dùng mTLS.

`pki.PinnedTLSConfig` là nơi duy nhất được dùng `InsecureSkipVerify`, kèm `VerifyConnection` tự kiểm tra chuỗi cert.

## 8. Giao thức agent

Mọi route agent dùng mTLS (TLS 1.3). Mỗi request cập nhật `last_seen`.

| Route | Nội dung |
| --- | --- |
| `POST /v1/enroll/join` | `{token, csr_pem}` → `{name, cert_pem, ca_pem, not_after, report_every_s}`; không cần client cert |
| `POST /v1/agent/report` | `{version, sent_at, status, host, backup_runs, allow}` → `{server_time, report_every_s, backup_run_cursor, renew}` |
| `POST /v1/agent/renew` | `{csr_pem}` → `{cert_pem, not_after}` |
| `GET /v1/agent/tasks?wait=25s` | long-poll → `[{id, action, params, result_by}]` |
| `POST /v1/agent/tasks/{id}/result` | `{state, started_at, finished_at, output, error}` → 204 |
| `POST /v1/agent/tasks/{id}/log` | một đoạn log của task (có giới hạn kích thước) |

- `backup_runs` là các dòng có `id` lớn hơn cursor, tối đa 500; cần `store.BackupRunsAfter` trên agent.
- `allow` là danh sách action mà agent đang cho phép; server từ chối sớm task không nằm trong đó.
- Một lần poll mới của cùng agent huỷ lần poll trước.
- Agent report mỗi 60 giây (lệch ±10%), poll liên tục. Gặp 401 thì dừng và ghi log, không lặp nóng.

## 9. Đăng nhập admin

Chỉ **một mật khẩu quản trị**. Không username, role, API token, cert hay MFA. Ai đăng nhập được thì có toàn quyền; giới hạn quyền nằm ở allowlist local của từng agent (mục 10).

- Băm bằng PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`), 600.000 vòng, salt 16 byte, lưu `pbkdf2-sha256$<vòng>$<salt>$<hash>`. So sánh bằng `subtle.ConstantTimeCompare`. Tối thiểu 12 ký tự.
- `sentinelctl login` hỏi mật khẩu (hoặc `--password-stdin`), gọi `POST /v1/admin/login`, nhận session token 256-bit. Session hết hạn sau 12 giờ, hoặc 1 giờ không dùng. Server chỉ lưu SHA-256 của token.
- Client giữ session ở `~/.config/sentinel/session` (0600); từ chối file có quyền rộng hơn.
- Chống dò mật khẩu: một IP sai 5 lần liên tiếp bị chặn 15 phút; toàn server không quá 30 lần sai mỗi giờ. Không khoá toàn cục, vì như vậy kẻ tấn công khoá được cả admin thật.
- `sentinelctl passwd` đổi mật khẩu và thu hồi mọi session. Quên mật khẩu thì chạy `sentinel-server passwd` bằng root trên máy server.
- Audit ghi người thao tác là `admin` cùng IP và 8 ký tự đầu của hash session.

`admin_listen` mặc định `127.0.0.1:9321`: dùng trên máy control plane hoặc qua SSH tunnel; có thể gắn vào địa chỉ WireGuard/Tailscale.

## 10. Action và allowlist

Bảng action cố định trong `pkg/agentapi`; tham số được decode bằng `DisallowUnknownFields` và validate cả ở server lẫn agent.

| Action | Tham số | Ghi chú |
| --- | --- | --- |
| `backup.start` / `backup.stop` | `job` | `job` phải có trong config local |
| `backup.snapshots` | `job` | chỉ đọc |
| `doctor.run` | — | kết quả đã `restic.Redact` |
| `service.start\|stop\|restart` | `unit` | regex `.service\|.timer`, khớp chính xác trong `allow.services`; không đụng `sentinel.service` |
| `container.restart` | `container`, `timeout_s` | khớp chính xác trong `allow.containers`; Docker Engine API qua unix socket |
| `os.check_updates` | — | `apt-get -s upgrade` hoặc `dnf check-update` |
| `os.apply_updates` | `security_only` | cần `allow.os_updates: apply`; xác nhận trước khi gửi |
| `agent.update` | `version` | cần `allow.self_update`; mục 12 |

Không có action nào chạy shell, nhận path, tên package hay lệnh tự do. Không có reboot.

Allowlist nằm trong `config.yaml` của agent, **mặc định tắt hết**:

```yaml
control_plane:
  url: https://cp.example.vn:9320
  allow:
    backup: false
    services: []
    containers: []
    docker_socket: /var/run/docker.sock
    os_updates: off            # off | check | apply
    self_update: {}            # public_key để bật
```

Agent kiểm tra lại mọi task theo allowlist này, trả `refused` nếu không đạt, giới hạn tần suất theo action, và ghi log local mỗi task, thứ mà control plane không xoá được.

`os.apply_updates` chạy qua `systemd-run --wait --pipe --collect --unit=sentinel-os-update -- /usr/bin/apt-get -y … upgrade` (argv cố định), vì `ProtectSystem=full` chặn daemon ghi `/usr`. Đây là lối ra khỏi sandbox có chủ ý, chỉ mở khi `os_updates: apply`.

## 11. Schema

SQLite (`modernc.org/sqlite`), migration nhúng bằng `embed` ở `server/internal/store/migrations/NNNN_<tên>.sql`, chỉ nối thêm; có test chặn sửa hay xoá file đã có.

| Migration | Bảng |
| --- | --- |
| 1 | `agents`, `join_tokens`, `certs`, `admin` (một dòng: `password_hash`, `changed_at`), `sessions`, `login_failures`, `audit_log` (trigger chặn UPDATE/DELETE, chuỗi hash), `alerts` |
| 2 | `agent_reports`, `host_samples`, `backup_runs` |
| 3 | `tasks` (queued/sent/succeeded/failed/refused/canceled/expired), `task_logs` |
| 4 | `schedules`, `maintenance` |

Audit: mỗi dòng có `hash = sha256(prev_hash ‖ nội dung chuẩn hoá)`; `sentinel-server audit verify` kiểm tra chuỗi. Mỗi entry cũng ghi `slog` để journald giữ bản thứ hai.

## 12. Tự cập nhật agent

- `pkg/release`: manifest `{version, os, arch, sha256, url}` ký ed25519.
- Khoá công khai nằm trong config local của agent, nên control plane bị chiếm cũng không đẩy được binary lạ.
- `agent.update` tải binary, kiểm tra chữ ký, sha256 và version (không cho hạ), ghi file tạm rồi `rename`, để systemd restart.
- Giữ binary cũ thành `sentinel.prev`; nếu bản mới không report được trong 5 phút thì lần start sau tự quay về bản cũ.
- `sentinelctl release keygen|sign`; `make dist` ký manifest khi có `SENTINEL_RELEASE_KEY`.

## 13. Cảnh báo

Control plane dùng lại `alert.Manager` và `notify` (chuyển sang `pkg`; `alert.Manager.Store` thành interface).

- `agent:<tên>` (Critical): `last_seen` cũ hơn `offline_after` (mặc định 2 phút); có thời gian ân hạn sau khi control plane khởi động.
- `agent-cert:<tên>` (Warning): cert còn dưới 14 ngày.
- Cửa sổ bảo trì tắt `agent:` và đặt trạng thái `MAINTENANCE`.
- Control plane **không** chuyển tiếp alert mà agent đã tự gửi; nó chỉ báo điều agent không tự báo được.
- Đặt `heartbeat.url` cho chính control plane để biết khi nó chết.

## 14. CLI và TUI

- `sentinel-server`: `init`, `serve`, `enroll`, `passwd`, `audit verify` (chạy bằng root trên máy control plane).
- `sentinel agent join|status` trên server.
- `sentinelctl`: `login`, `logout`, `passwd`, `server ls|add|revoke|show`, `alert`, `task`, `run <server> <action> k=v [--wait|--follow]`, `audit`, `schedule`, `maintenance`, `report`, `release`; có `-o json`. Không lệnh con thì mở TUI.
- `--follow` và màn hình task dùng SSE (`GET /v1/admin/tasks/{id}/events`).
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
| Dò mật khẩu admin | Loopback mặc định, TLS 1.3, PBKDF2, chặn theo IP, trần toàn server, session ngắn hạn lưu hash, audit mọi lần đăng nhập |
| Sửa audit | Trigger append-only, chuỗi hash, bản sao journald. Root trên máy control plane vẫn sửa được DB: rủi ro còn lại, ghi rõ |
| Text độc trong report hoặc log | `termtext.Clean`, `restic.Redact`, `MaxBytesReader`, giới hạn độ dài |
| Tiêm tham số | Regex, khớp chính xác allowlist, argv slice kèm `--`, `url.PathEscape` cho Docker |
| Secret lọt qua danh sách tiến trình | Chỉ thu tên và user của tiến trình, không bao giờ cmdline hay env |
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
| 6 | `httpapi`, join, `sentinelctl login\|server\|audit` |
| 7 | Report, cảnh báo offline, gia hạn cert |
| 8 | TUI của `sentinelctl` |
| 9 | Task engine và action backup, log qua SSE |
| 10 | Xoá mô hình pull (mục 15) |
| 11 | Action systemd và Docker |
| 12 | Cập nhật OS |
| 13 | Lịch và cửa sổ bảo trì |
| 14 | Báo cáo và lịch sử |
| 15 | Tự cập nhật agent |

## 18. Thư viện

Không thêm dependency. Dùng `crypto/*`, `net/http`, `embed`, `testing/synctest` (stdlib), cùng `robfig/cron/v3`, `cenkalti/backoff/v5` và `gopsutil/v4` đã có trong `go.mod`. Docker qua Engine API bằng `net/http` trên unix socket, không dùng Docker SDK; systemd qua `systemctl`, không dùng go-systemd.

## 19. Câu hỏi mở

1. Có chuyển tiếp alert local của agent qua control plane không? Đề xuất: không.
2. Một control plane không HA có đủ cho 5 đến 50 máy không? Đề xuất: có.
3. Xoay CA ở v1 làm thế nào? Đề xuất: join lại thủ công từng agent.
