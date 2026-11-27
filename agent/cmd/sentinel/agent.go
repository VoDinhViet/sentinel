package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"sentinel/agent/internal/transport"
)

// defaultAgentDir is where the identity goes when no config tells where the state lives.
const defaultAgentDir = "/var/lib/sentinel/" + transport.DirName

func (a *cliApp) agentCmd() *cobra.Command {
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Kết nối server này với control plane",
		Long: `Server này chủ động kết nối ra control plane (sentinel-server), không mở cổng nào để nhận kết nối vào.
Lệnh "agent join" xin chứng chỉ bằng join token; chạy một lần cho mỗi server.`,
	}
	agentCmd.AddCommand(a.newAgentJoinCmd())
	return agentCmd
}

func (a *cliApp) newAgentJoinCmd() *cobra.Command {
	var opts transport.JoinOptions
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join control plane bằng join token để lấy chứng chỉ của server này",
		Long: `Tạo khoá riêng trên máy này (khoá không rời máy), gửi yêu cầu chứng chỉ kèm join token cho control plane, rồi lưu khoá, chứng chỉ
và CA vào thư mục agent trong thư mục state (mặc định cạnh file store của config, hoặc ` + defaultAgentDir + `).

Chỉ tin control plane có CA khớp --ca-fingerprint (do "sentinel-server init" in ra), nên không phải chép file CA.
Tên của server do control plane đặt theo token, không phải do máy này khai.

Join token dùng một lần và hết hạn sau 24 giờ. Truyền bằng --token thì nó hiện trong danh sách tiến trình (ps) trong lúc lệnh chạy;
trên máy nhiều người dùng thì dùng --token-stdin hoặc để trống để được hỏi.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.URL == "" || opts.CAFingerprint == "" {
				return errors.New("cần --url và --ca-fingerprint (lệnh đầy đủ do sentinelctl server add in ra)")
			}
			token, err := a.joinToken(cmd, opts.Token, tokenStdin)
			if err != nil {
				return err
			}
			opts.Token = token
			dir, note := a.agentDir(opts.Dir)
			opts.Dir = dir
			out := cmd.OutOrStdout()
			if note != "" {
				fmt.Fprintln(out, note)
			}
			joined, err := transport.Join(cmd.Context(), opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Đã join với tên %q. Chứng chỉ hết hạn %s; khoá, chứng chỉ và CA nằm trong %s.\n",
				joined.Name, joined.NotAfter.Local().Format("2006-01-02"), dir)
			who, err := transport.WhoAmI(cmd.Context(), dir, opts.URL)
			if err != nil {
				return fmt.Errorf("đã lưu chứng chỉ nhưng chưa gọi lại control plane được bằng nó: %w", err)
			}
			fmt.Fprintf(out, "Control plane nhận ra server này là %q.\n", who.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.URL, "url", "", "địa chỉ cổng agent của control plane, ví dụ https://ctl.example.com:9320")
	cmd.Flags().StringVar(&opts.Token, "token", "", "join token (xem cảnh báo về ps ở trên)")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "đọc join token từ stdin")
	cmd.Flags().StringVar(&opts.CAFingerprint, "ca-fingerprint", "", "fingerprint của CA (sha256:…) do sentinel-server init in ra")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "thư mục lưu khoá và chứng chỉ (mặc định: agent/ trong thư mục state)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "thay danh tính đã có (cần join token mới)")
	return cmd
}

// joinToken is the token from the flag, from stdin, or typed.
func (a *cliApp) joinToken(cmd *cobra.Command, flagged string, fromStdin bool) (string, error) {
	switch {
	case flagged != "" && fromStdin:
		return "", errors.New("chỉ dùng một trong --token và --token-stdin")
	case flagged != "":
		return flagged, nil
	case fromStdin:
		return readTokenLine(cmd.InOrStdin())
	case a.term.isTerminal():
		token, err := a.term.readSecret("Join token: ")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(token), nil
	}
	return "", errors.New("cần join token: --token, --token-stdin hoặc nhập trên terminal")
}

func readTokenLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("đọc join token: %w", err)
	}
	if token := strings.TrimSpace(line); token != "" {
		return token, nil
	}
	return "", errors.New("không đọc được join token từ stdin")
}

// agentDir is where the identity goes: --dir, or "agent" next to the store of the config. Without a
// readable config it is the default, and the note says so.
func (a *cliApp) agentDir(flagged string) (dir, note string) {
	if flagged != "" {
		return flagged, ""
	}
	cfg, err := a.readConfig()
	if err != nil {
		return defaultAgentDir, fmt.Sprintf("Chưa đọc được config (%v): lưu vào %s.", err, defaultAgentDir)
	}
	return filepath.Join(filepath.Dir(cfg.StorePath), transport.DirName), ""
}
