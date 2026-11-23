package main

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// The palette has a dark and a light variant: the dark one until the terminal says what its
// background is (tea.BackgroundColorMsg), then applyPalette picks the one that suits. The terminal downsamples the colours itself when it has fewer than 24 bits.
var (
	colourOK, colourWarn, colourBad, colourAcc color.Color
	colourSelected, colourBorder, colourMuted  color.Color
	colourBar                                  color.Color
	colourOnAccent, colourOnSelected           color.Color

	titleStyle, dimStyle, headerStyle, borderStyle lipgloss.Style
	// The selected line is painted with a background.
	selectedStyle       lipgloss.Style
	activeTab           lipgloss.Style
	keyStyle            lipgloss.Style
	lipglossStyleAccent lipgloss.Style
	// The key bar at the bottom and the header of a table are bands with their own background.
	barStyle, barKeyStyle, barDimStyle, headerBand lipgloss.Style
)

func init() { applyPalette(true) }

// applyPalette sets every colour and style of the screens for a dark or a light background.
func applyPalette(dark bool) {
	pick := lipgloss.LightDark(dark)
	hex := lipgloss.Color
	colourAcc = pick(hex("#2e59d9"), hex("#7aa2f7"))
	colourOK = pick(hex("#1f7a35"), hex("#9ece6a"))
	colourWarn = pick(hex("#946200"), hex("#e0af68"))
	colourBad = pick(hex("#c62828"), hex("#f7768e"))
	colourMuted = pick(hex("#5c6384"), hex("#8089b3"))
	colourBorder = pick(hex("#8a94c4"), hex("#3f4768"))
	colourSelected = pick(hex("#e2e4ea"), hex("#30333d"))
	colourBar = pick(hex("#eceef2"), hex("#23252d"))
	colourOnSelected = pick(hex("#16171c"), hex("#eceef4"))
	colourOnAccent = pick(hex("#ffffff"), hex("#14162b"))

	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle = lipgloss.NewStyle().Foreground(colourMuted)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colourMuted)
	borderStyle = lipgloss.NewStyle().Foreground(colourBorder)
	selectedStyle = lipgloss.NewStyle().Background(colourSelected).Foreground(colourOnSelected)
	activeTab = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colourAcc)
	keyStyle = lipgloss.NewStyle().Bold(true).Foreground(colourAcc)
	lipglossStyleAccent = lipgloss.NewStyle().Foreground(colourAcc)
	barStyle = lipgloss.NewStyle().Background(colourBar)
	barKeyStyle = barStyle.Bold(true).Foreground(colourAcc)
	barDimStyle = barStyle.Foreground(colourMuted)
	headerBand = barStyle.Bold(true).Foreground(colourMuted)
}

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
	return severityStyle(level).Background(colourSelected)
}

// badge is a short status word on a coloured background, e.g. " FAILED ".
func badge(level severity, text string) string {
	background := map[severity]color.Color{sevOK: colourOK, sevWarn: colourWarn, sevBad: colourBad}[level]
	return lipgloss.NewStyle().Bold(true).Foreground(colourOnAccent).Background(background).Render(" " + text + " ")
}
