package matching

import "regexp"

// NOTE: deliberately does not include the bare word "version" - it's too
// generic and false-positives on legitimate, non-remixed compilation/
// soundtrack qualifiers like "(Soundtrack Version)".
var (
	remixKeywords           = regexp.MustCompile(`(?i)\b(remix|remixed|edit|mix|acoustic|live|instrumental|radio edit|bootleg|dub|extended|vip|flip|rework|reimagined)\b`)
	remasterKeywords        = regexp.MustCompile(`(?i)\b(remaster(?:ed)?)\b`)
	alternateVersionKeyword = regexp.MustCompile(`(?i)\b(unplugged|acoustic|live|instrumental|radio edit|session|performance|cover)\b`)
	demoKeywords            = regexp.MustCompile(`(?i)\b(demo)\b`)
	rerecordedKeywords      = regexp.MustCompile(`(?i)\b(re[- ]?recorded|re[- ]?recording|re[- ]?record|taylor'?s? version)\b`)
	speedModifiedKeywords   = regexp.MustCompile(`(?i)\b(sped up|speed up|slowed down|slow down|nightcore|slowed \+ reverb|sped \+ reverb|accelerated|decelerated)\b`)
)

func hasRemixIndicator(title string) bool    { return remixKeywords.MatchString(title) }
func hasRemasterIndicator(title string) bool { return remasterKeywords.MatchString(title) }
func hasAlternateVersionIndicator(title string) bool {
	return alternateVersionKeyword.MatchString(title)
}
func hasDemoIndicator(title string) bool          { return demoKeywords.MatchString(title) }
func hasReRecordedIndicator(title string) bool    { return rerecordedKeywords.MatchString(title) }
func hasSpeedModifiedIndicator(title string) bool { return speedModifiedKeywords.MatchString(title) }
