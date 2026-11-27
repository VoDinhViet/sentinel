# Nơi lưu backup 100 GB, thanh toán bằng USDC

## Đề xuất: Cloudflare R2, trả bằng USDC trên Base hoặc Polygon

Khoảng **$1.4–1.5 một tháng** (≈ $16.4–18.2 một năm). Phí mạng thấp, restore miễn phí, `restic prune` không bị phạt, và tự trừ tiền định kỳ từ ví. Chuyển sang VPS chỉ đáng khi repo vượt khoảng 133 GB.

## Bảng so sánh

Đối chiếu trang chính thức ngày 2026-11-26. Cột "thực tế" gồm giá niêm yết, phí mạng và một lần restore thử mỗi năm, chưa gồm thuế. Phần phí mạng là ước tính vì phí gas và phí rút USDC từ sàn đổi theo thời điểm.

| # | Phương án | Loại | Tháng (thực tế) | Năm (thực tế) | USDC nhận trên mạng | Restore | Lưu ý | Link xem nhanh |
|---|---|---|---|---|---|---|---|---|
| **1** | **Cloudflare R2** | S3 | **≈ $1.4–1.5** | **≈ $16.4–18.2** | Base, Polygon | **Miễn phí** | Chưa rõ tài khoản Free hay Pro có trả bằng crypto được không, cần thử ở bước checkout | [Giá](https://developers.cloudflare.com/r2/pricing/) · [Thanh toán USDC](https://developers.cloudflare.com/billing/payment-methods/stablecoin-payments/) |
| 2 | Storj | S3 | ≈ $0.77–2.0 | ≈ $9.2–24 | **Chỉ Ethereum L1**, nạp trước | $7/TB | Lưu tối thiểu 30 ngày. Phí nạp L1 dao động rất lớn nên có thể đắt hơn R2 | [Giá](https://storj.dev/dcs/pricing/simplified) · [Thanh toán](https://storj.dev/support/account-management-billing/payment-methods) |
| 3 | AlphaVPS Storage 256 GB | VPS | ≈ $1.83–1.92 | ≈ $22–23 | **Chưa xác nhận**, trang chỉ ghi "crypto" | Trong 1 TB traffic của gói | Phải tự vận hành máy. Giá gia hạn chưa kiểm tra | [Giá và thanh toán](https://alphavps.com/storage-vps.html) |
| 4 | Vultr Object Storage (tier Archive) | S3 | $6 | $72 | BitPay (có USDC) | Trong 1 TB băng thông của gói | Chưa rõ restic có hợp tier archive không | [Tài liệu](https://docs.vultr.com/products/storage/object-storage/provisioning) · [Thanh toán](https://docs.vultr.com/platform/billing/faq) |
| 5 | Vultr VPS rẻ nhất + HDD 100 GB | VPS | ≈ $6 | ≈ $72 | BitPay (có USDC) | Trong băng thông của gói | Giá HDD ($25/TB) chưa kiểm chứng trực tiếp. VPS chỉ 512 MB RAM | [Giá VPS](https://api.vultr.com/v2/plans?type=vc2&per_page=500) · [Datasheet Block Storage](https://discover.vultr.com/block-storage-datasheet) |
| 6 | Vultr High Frequency 128 GB | VPS | $24 | $288 | BitPay (có USDC) | 3 TB traffic | Dư khoảng 28 GB sau khi trừ dữ liệu, khá sát | [API giá](https://api.vultr.com/v2/plans?type=vhf&per_page=500) |
| 7 | Cherry Servers Storage VPS | VPS | từ $22.23 | ≥ $267 | USDC qua CoinGate (không có USDT) | 35 TB traffic | Không tìm thấy giá theo dung lượng | [Crypto](https://www.cherryservers.com/buy-vps-with-crypto) · [Thanh toán](https://www.cherryservers.com/knowledge/docs/usage-billing/payment-methods) |
