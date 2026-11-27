package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// level is how a line of a table is doing, shown as a colour.
type level int

const (
	levelNone level = iota // not painted
	levelOK
	levelWarn
	levelBad
	levelDim
)

// The palette has a dark and a light variant: the dark one until the terminal says what its
// background is (tea.BackgroundColorMsg), then applyPalette picks the one that suits.
var (
	colourOK, colourWarn, colourBad, colourAccent, colourMuted   color.Color
	colourSelected, colourOnSelected, colourOnAccent, colourRule color.Color

	titleStyle, dimStyle, headerStyle, ruleStyle, selectedStyle lipgloss.Style
	activeTabStyle, keyStyle                                    lipgloss.Style
)

func init() { applyPalette(true) }

// applyPalette sets every colour and style for a dark or a light background.
func applyPalette(dark bool) {
	pick := lipgloss.LightDark(dark)
	hex := lipgloss.Color
	colourAccent = pick(hex("#2e59d9"), hex("#7aa2f7"))
	colourOK = pick(hex("#1f7a35"), hex("#9ece6a"))
	colourWarn = pick(hex("#946200"), hex("#e0af68"))
	colourBad = pick(hex("#c62828"), hex("#f7768e"))
	colourMuted = pick(hex("#5c6384"), hex("#8089b3"))
	colourRule = pick(hex("#8a94c4"), hex("#3f4768"))
	colourSelected = pick(hex("#e2e4ea"), hex("#30333d"))
	colourOnSelected = pick(hex("#16171c"), hex("#eceef4"))
	colourOnAccent = pick(hex("#ffffff"), hex("#14162b"))

	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle = lipgloss.NewStyle().Foreground(colourMuted)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colourMuted)
	ruleStyle = lipgloss.NewStyle().Foreground(colourRule)
	selectedStyle = lipgloss.NewStyle().Background(colourSelected).Foreground(colourOnSelected)
	activeTabStyle = lipgloss.NewStyle().Bold(true).Background(colourAccent).Foreground(colourOnAccent)
	keyStyle = lipgloss.NewStyle().Bold(true).Foreground(colourAccent)
}

func levelStyle(of level) lipgloss.Style {
	switch of {
	case levelBad:
		return lipgloss.NewStyle().Foreground(colourBad).Bold(true)
	case levelWarn:
		return lipgloss.NewStyle().Foreground(colourWarn).Bold(true)
	case levelDim:
		return lipgloss.NewStyle().Foreground(colourMuted)
	}
	return lipgloss.NewStyle().Foreground(colourOK)
}
