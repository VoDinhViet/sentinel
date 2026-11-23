package main

import (
	"charm.land/lipgloss/v2"
)

var (
	colourOK   = lipgloss.Color("2")
	colourWarn = lipgloss.Color("3")
	colourBad  = lipgloss.Color("1")
	colourAcc  = lipgloss.Color("4")

	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Faint(true)
	// The selected line is painted with a background; the severity colours on it are the
	// bright ones so that red, green and yellow stay readable on that background.
	colourSelected      = lipgloss.Color("18")
	selectedStyle       = lipgloss.NewStyle().Background(colourSelected).Foreground(lipgloss.Color("15"))
	activeTab           = lipgloss.NewStyle().Bold(true).Foreground(colourAcc)
	keyStyle            = lipgloss.NewStyle().Bold(true).Foreground(colourAcc)
	lipglossStyleAccent = lipgloss.NewStyle().Foreground(colourAcc)
)

func severityStyle(level severity) lipgloss.Style {
	switch level {
	case sevBad:
		return lipgloss.NewStyle().Foreground(colourBad).Bold(true)
	case sevWarn:
		return lipgloss.NewStyle().Foreground(colourWarn).Bold(true)
	}
	return lipgloss.NewStyle().Foreground(colourOK)
}

// selectedSeverityStyle is severityStyle on the selected line's background.
func selectedSeverityStyle(level severity) lipgloss.Style {
	bright := map[severity]string{sevOK: "10", sevWarn: "11", sevBad: "9"}[level]
	return selectedStyle.Foreground(lipgloss.Color(bright)).Bold(level != sevOK)
}
