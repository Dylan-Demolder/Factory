package tui

import "github.com/charmbracelet/lipgloss"

// The palette mirrors internal/web/static/style.css so the terminal and the
// browser read as the same product, in light and dark terminals alike.
var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#4f46e5", Dark: "#8b85ff"}
	colorText   = lipgloss.AdaptiveColor{Light: "#1c2030", Dark: "#e6e8f0"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#69708a", Dark: "#8d93a8"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#4ade80"}
	colorBad    = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f87171"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	colorInfo   = lipgloss.AdaptiveColor{Light: "#0369a1", Dark: "#38bdf8"}
	colorPanel  = lipgloss.AdaptiveColor{Light: "#f1f3f9", Dark: "#1e2230"}
	colorBorder = lipgloss.AdaptiveColor{Light: "#e3e6ef", Dark: "#2a2f40"}
)

var (
	logoStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	routeStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(0, 1)

	topbarStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorBorder)

	sectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMuted).
			Padding(1, 0, 0, 1)

	sidebarStyle = lipgloss.NewStyle().
			BorderRight(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorBorder).
			PaddingRight(1)

	itemStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(0, 1)

	selectedItemStyle = lipgloss.NewStyle().
				Foreground(colorText).
				Background(colorAccent).
				Background(lipgloss.AdaptiveColor{Light: "#eceafd", Dark: "#25234a"}).
				Bold(true).
				Padding(0, 1)

	mutedStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle = lipgloss.NewStyle().Foreground(colorAccent)
	okStyle     = lipgloss.NewStyle().Foreground(colorOK)
	badStyle    = lipgloss.NewStyle().Foreground(colorBad)
	warnStyle   = lipgloss.NewStyle().Foreground(colorWarn)
	infoStyle   = lipgloss.NewStyle().Foreground(colorInfo)

	helpStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	statusStyle = lipgloss.NewStyle().
			Foreground(colorText).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	filterStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true).
			PaddingLeft(1)

	paletteBoxStyle = lipgloss.NewStyle().
			Background(colorPanel).
			Foreground(colorText).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)

	paletteSelStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	paletteStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	helpBoxStyle = lipgloss.NewStyle().
			Background(colorPanel).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	cardStyle = lipgloss.NewStyle().
			Background(colorPanel).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	h1Style    = lipgloss.NewStyle().Bold(true).Foreground(colorText)

	tabStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(0, 1)

	activeTabStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true).
			Underline(true).
			Padding(0, 1)
)

// pill renders a status chip. Only the side borders are drawn: a full rounded
// box is three lines tall, and an inline chip that tall tears rows apart.
func pill(color lipgloss.TerminalColor, text string) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderForeground(color).
		Padding(0, 1).
		Render(text)
}

// phasePill colours a project phase the same way the web stepper does.
func phasePill(phase string, running bool) string {
	if running {
		return pill(colorInfo, "● "+phase)
	}
	switch phase {
	case "done":
		return pill(colorOK, "✔ done")
	case "spec":
		return pill(colorWarn, "spec")
	default:
		return pill(colorMuted, phase)
	}
}
