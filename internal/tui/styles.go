package tui

import "github.com/charmbracelet/lipgloss"

// ─── Colors (Catppuccin Mocha-inspired palette) ──────────────────────────────

var (
	colorBase     = lipgloss.Color("#1e1e2e") // Background
	colorSurface  = lipgloss.Color("#313244") // Surface
	colorOverlay  = lipgloss.Color("#45475a") // Borders
	colorText     = lipgloss.Color("#cdd6f4") // Main text
	colorSubtext  = lipgloss.Color("#a6adc8") // Secondary text
	colorLavender = lipgloss.Color("#b4befe") // Primary accent
	colorGreen    = lipgloss.Color("#a6e3a1") // Success / counts
	colorPeach    = lipgloss.Color("#fab387") // Warnings / types
	colorRed      = lipgloss.Color("#f38ba8") // Errors
	colorBlue     = lipgloss.Color("#89b4fa") // Links / IDs
	colorMauve    = lipgloss.Color("#cba6f7") // Highlights
	colorYellow   = lipgloss.Color("#f9e2af") // Session info
	colorTeal     = lipgloss.Color("#94e2d5") // Search highlights
)

// ─── Layout Styles ───────────────────────────────────────────────────────────

var (
	// App frame
	appStyle = lipgloss.NewStyle().
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
			BorderStyle(lipgloss.RoundedBorder()).
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

	// Active session badge
	activeBadgeStyle = lipgloss.NewStyle().
				Foreground(colorGreen).
				Bold(true)
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
				BorderStyle(lipgloss.RoundedBorder()).
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
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorLavender).
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
