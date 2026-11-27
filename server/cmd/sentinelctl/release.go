package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"sentinel/pkg/release"
)

func newReleaseCmd() *cobra.Command {
	releaseCmd := &cobra.Command{
		Use:   "release",
		Short: "Khóa và manifest ký cho agent.update (chạy trên máy build, không cần đăng nhập)",
		Long: `Agent tự cập nhật chỉ tin một manifest ký bằng khóa công khai ghi trong config của chính nó
(control_plane.allow.self_update.public_key). control plane chỉ nói số version, nên control plane bị chiếm cũng không đưa được binary.

  sentinelctl release keygen --out release.key        tạo cặp khóa, in khóa công khai để đưa vào config của agent
  sentinelctl release sign --key release.key …        ký manifest cho một binary

Giữ release.key ở máy build, không đưa lên control plane hay vào repo.`,
	}
	releaseCmd.AddCommand(newReleaseKeygenCmd(), newReleaseSignCmd())
	return releaseCmd
}

func newReleaseKeygenCmd() *cobra.Command {
	var out string
	keygenCmd := &cobra.Command{
		Use:   "keygen --out <file>",
		Short: "Tạo cặp khóa ed25519 để ký bản phát hành",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("--out là file lưu khóa bí mật, ví dụ release.key")
			}
			public, private, err := release.GenerateKey()
			if err != nil {
				return err
			}
			// O_EXCL: a key that exists is never overwritten, and it is made 0600 from the start.
			file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("tạo file khóa: %w", err)
			}
			if _, err := file.WriteString(release.EncodeKey(private) + "\n"); err != nil {
				_ = file.Close()
				_ = os.Remove(out)
				return fmt.Errorf("ghi file khóa: %w", err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("ghi file khóa: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Khóa bí mật: %s (quyền 0600; giữ ở máy build)\nKhóa công khai, đưa vào config của agent:\n\n  control_plane:\n    allow:\n      self_update:\n        public_key: %s\n", out, release.EncodeKey(public))
			return nil
		},
	}
	keygenCmd.Flags().StringVar(&out, "out", "", "file lưu khóa bí mật (không ghi đè file đã có)")
	return keygenCmd
}

func newReleaseSignCmd() *cobra.Command {
	var keyFile, binary, version, goos, goarch, address, out string
	signCmd := &cobra.Command{
		Use:   "sign --key <file> --binary <file> --version 1.2.0 --url <https://…>",
		Short: "Ký manifest của một binary: version, OS, arch, sha256, kích thước, địa chỉ",
		Example: `  sentinelctl release sign --key release.key --binary dist/sentinel_linux_amd64 --version 1.2.0 \
    --url https://releases.example.com/sentinel/1.2.0/sentinel_linux_amd64 --out dist/manifest-1.2.0-linux-amd64.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if keyFile == "" || binary == "" || version == "" || address == "" {
				return errors.New("cần --key, --binary, --version và --url")
			}
			if err := release.CheckURL(address); err != nil {
				return err
			}
			private, err := readPrivateKey(keyFile)
			if err != nil {
				return err
			}
			file, err := os.Open(binary)
			if err != nil {
				return fmt.Errorf("mở binary: %w", err)
			}
			defer file.Close()
			sum, size, err := release.Sum(file)
			if err != nil {
				return fmt.Errorf("tính sha256 của binary: %w", err)
			}
			signed, err := release.Sign(private, release.Manifest{Version: version, OS: goos, Arch: goarch, SHA256: sum, Size: size, URL: address})
			if err != nil {
				return err
			}
			if out == "" {
				_, err = cmd.OutOrStdout().Write(append(signed, '\n'))
				return err
			}
			if err := os.WriteFile(out, append(signed, '\n'), 0o644); err != nil { //nolint:gosec // a manifest is public
				return fmt.Errorf("ghi manifest: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Đã ký %s (%s/%s, %d byte, sha256 %s…)\n", out, goos, goarch, size, sum[:12])
			return nil
		},
	}
	flags := signCmd.Flags()
	flags.StringVar(&keyFile, "key", "", "file khóa bí mật (từ release keygen)")
	flags.StringVar(&binary, "binary", "", "binary cần ký")
	flags.StringVar(&version, "version", "", "version dạng 1.2.0, không có chữ v")
	flags.StringVar(&goos, "os", runtime.GOOS, "hệ điều hành của binary")
	flags.StringVar(&goarch, "arch", runtime.GOARCH, "kiến trúc của binary")
	flags.StringVar(&address, "url", "", "địa chỉ https nơi agent tải binary")
	flags.StringVar(&out, "out", "", "file manifest (mặc định in ra stdout)")
	return signCmd
}

// readPrivateKey reads a key file, and refuses one that others can read: it signs what agents will run as root.
func readPrivateKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("đọc file khóa: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("file khóa %s có quyền %v: người khác đọc được; chmod 600 %s", path, info.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("đọc file khóa: %w", err)
	}
	return release.ParsePrivateKey(string(raw))
}
