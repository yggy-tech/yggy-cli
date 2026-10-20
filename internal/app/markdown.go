package app

import "regexp"

var (
	markdownBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	markdownHeading = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+)$`)
)

const (
	boldOn  = "\033[1m"
	boldOff = "\033[22m"
)

// render shows the agent's Markdown emphasis and headings as bold on a terminal.
// Anywhere else the text stays Markdown, for scripts and files.
func (a *App) render(text string) string {
	if !a.Styled {
		return text
	}
	text = markdownHeading.ReplaceAllString(text, boldOn+"$1"+boldOff)
	return markdownBold.ReplaceAllString(text, boldOn+"$1"+boldOff)
}
