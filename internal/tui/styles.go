package tui

import "github.com/charmbracelet/lipgloss"

// ─── Colors (Engram Elephant palette) ─────────────────────────────────────────

var (
	colorBase     = lipgloss.Color("#191724") // Deep purple/black base
	colorSurface  = lipgloss.Color("#1f1d2e") // Slightly lighter panel bg
	colorOverlay  = lipgloss.Color("#6e6a86") // Muted purple borders
	colorText     = lipgloss.Color("#e0def4") // Light lavender text
	colorSubtext  = lipgloss.Color("#908caa") // Dim lavender
	colorLavender = lipgloss.Color("#c4a7e7") // Primary brand purple
	colorGreen    = lipgloss.Color("#9ccfd8") // Cyan/Teal for "success" (matches lightning)
	colorPeach    = lipgloss.Color("#f6c177") // Warm accent
	colorRed      = lipgloss.Color("#eb6f92") // Soft red
	colorBlue     = lipgloss.Color("#31748f") // Deep cyan
	colorMauve    = lipgloss.Color("#ebbcba") // Soft pink/mauve
	colorYellow   = lipgloss.Color("#f1ca93") // Gold
	colorTeal     = lipgloss.Color("#9ccfd8") // Bright Cyan (Lightning)
)

// ─── Layout Styles ───────────────────────────────────────────────────────────

var (
	// App frame
	appStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(1, 2)

	// Header bar
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorLavender).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(colorOverlay).
			PaddingBottom(1).
			MarginBottom(1)

	// Footer / help bar
	helpStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			MarginTop(1)

	// Error message
	errorStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true).
			Padding(0, 1)

	// Clipboard copy feedback ("✓ Copied!")
	copyFeedbackStyle = lipgloss.NewStyle().
				Foreground(colorGreen).
				Bold(true).
				Padding(0, 1)

	// Update available banner
	updateBannerStyle = lipgloss.NewStyle().
				Foreground(colorYellow).
				Bold(true).
				Padding(0, 1)
)

// ─── Dashboard Styles ────────────────────────────────────────────────────────

var (
	// Big stat number
	statNumberStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorGreen).
			Width(8).
			Align(lipgloss.Right)

	// Stat label
	statLabelStyle = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(2)

	// Stat card container
	statCardStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorOverlay).
			Padding(1, 2).
			MarginBottom(1)

	// Menu item (normal)
	menuItemStyle = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(2)

	// Menu item (selected)
	menuSelectedStyle = lipgloss.NewStyle().
				Foreground(colorLavender).
				Bold(true).
				PaddingLeft(1)

	// Dashboard title
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMauve).
			MarginBottom(1)
)

// ─── List Styles ─────────────────────────────────────────────────────────────

var (
	// List item (normal)
	listItemStyle = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(2)

	// List item (selected/cursor)
	listSelectedStyle = lipgloss.NewStyle().
				Foreground(colorLavender).
				Bold(true).
				PaddingLeft(1)

	// Observation type badge
	typeBadgeStyle = lipgloss.NewStyle().
			Foreground(colorPeach).
			Bold(true)

	// Observation lifecycle warning badge
	stateWarningBadgeStyle = lipgloss.NewStyle().
				Foreground(colorYellow).
				Bold(true)

	// Observation ID
	idStyle = lipgloss.NewStyle().
		Foreground(colorBlue)

	// Timestamp
	timestampStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			Italic(true)

	// Project name
	projectStyle = lipgloss.NewStyle().
			Foreground(colorYellow)

	// Content preview
	contentPreviewStyle = lipgloss.NewStyle().
				Foreground(colorSubtext).
				PaddingLeft(4)
)

// ─── Detail View Styles ──────────────────────────────────────────────────────

var (
	// Section heading in detail views
	sectionHeadingStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorMauve).
				MarginTop(1).
				MarginBottom(1)

	// Detail content
	detailContentStyle = lipgloss.NewStyle().
				Foreground(colorText).
				PaddingLeft(2)

	// Detail label
	detailLabelStyle = lipgloss.NewStyle().
				Foreground(colorSubtext).
				Width(14).
				Align(lipgloss.Right).
				PaddingRight(1)

	// Detail value
	detailValueStyle = lipgloss.NewStyle().
				Foreground(colorText)
)

// ─── Timeline Styles ─────────────────────────────────────────────────────────

var (
	// Timeline focus observation
	timelineFocusStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(colorLavender).
				Padding(0, 1)

	// Timeline before/after items
	timelineItemStyle = lipgloss.NewStyle().
				Foreground(colorSubtext).
				PaddingLeft(2)

	// Timeline arrow connector
	timelineConnectorStyle = lipgloss.NewStyle().
				Foreground(colorOverlay)
)

// ─── Search Styles ───────────────────────────────────────────────────────────

var (
	searchInputStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(colorLavender).
				Foreground(colorText).
				Padding(0, 1).
				MarginBottom(1)

	searchHighlightStyle = lipgloss.NewStyle().
				Foreground(colorTeal).
				Bold(true)

	noResultsStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			Italic(true).
			PaddingLeft(2).
			MarginTop(1)
)
