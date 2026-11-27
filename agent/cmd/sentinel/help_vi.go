package main

import (
	"strings"

	"github.com/spf13/cobra"
)

// usageHeadings are the fixed words of cobra's usage template, in Vietnamese.
var usageHeadings = strings.NewReplacer(
	"Usage:", "Cách dùng:",
	"Aliases:", "Tên khác:",
	"Examples:", "Ví dụ:",
	"Available Commands:", "Các lệnh:",
	"Additional Commands:", "Lệnh khác:",
	"Global Flags:", "Cờ dùng chung:",
	"Flags:", "Cờ:",
	"Additional help topics:", "Chủ đề trợ giúp khác:",
	`Use "{{.CommandPath}} [command] --help" for more information about a command.`, `Dùng "{{.CommandPath}} [lệnh] --help" để xem chi tiết một lệnh.`,
)

// localizeHelp puts the help and version flags, the help command, the shell completion command
// and the headings of the usage text in Vietnamese; the descriptions of our own commands are
// written in Vietnamese where they are defined.
func localizeHelp(root *cobra.Command) {
	root.SetUsageTemplate(usageHeadings.Replace(root.UsageTemplate()))
	root.SetHelpCommand(&cobra.Command{
		Use:   "help [lệnh]",
		Short: "Xem trợ giúp của một lệnh",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, _, err := cmd.Root().Find(args)
			if err != nil || target == nil {
				return cmd.Root().Help()
			}
			return target.Help()
		},
	})
	root.CompletionOptions.HiddenDefaultCmd = true // still works, but is not listed
	root.InitDefaultVersionFlag()
	if flag := root.Flags().Lookup("version"); flag != nil {
		flag.Usage = "xem phiên bản"
	}
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Flags().Lookup("help") == nil {
			cmd.Flags().BoolP("help", "h", false, "xem trợ giúp về lệnh "+cmd.Name())
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
