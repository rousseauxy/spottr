package spotnet

import (
	"regexp"
	"strings"
)

var (
	// [b], [/b], [color="#FF0000"], [url=...]: markup around a label, never part of a value.
	bbTag = regexp.MustCompile(`\[/?[a-zA-Z]+(=[^\]]*)?\]`)
	// A label, a colon or equals sign, and the rest of the line. \b keeps words that only start
	// with a label out ("Wachtwoordgebruik", "passend").
	passwordLine = regexp.MustCompile(`(?i)\b(wachtwoord|password|passwort|pass|pw|ww)\b\s*[:=]\s*(.+)$`)
)

// Words that mean "there is no password". Measured 2026-10-15 on the spots with a description:
// of 27 mentioning a password, 26 said there was none ("Geen wachtwoord nodig", "Niet nodig",
// "deze download is zonder wachtwoord geplaatst") and one gave one ("Wachtwoord: KKKK2").
var noPassword = []string{"geen", "niet", "zonder", "none", "nvt", "n/a", "not needed", "no password", "nodig", "required", "benodigd"}

// PasswordHint returns the archive password a poster wrote in a spot's description, or "" when
// none is given. It is a suggestion for the person sending the spot, who sees and can change it;
// a sentence or a phrase saying there is no password is never returned.
func PasswordHint(description string) string {
	text := strings.NewReplacer("[br]", "\n", "[BR]", "\n", "<br>", "\n", "<br/>", "\n", "<br />", "\n", "\r", "").Replace(description)
	text = bbTag.ReplaceAllString(text, "")
	for _, line := range strings.Split(text, "\n") {
		m := passwordLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		value := strings.Trim(strings.TrimSpace(m[2]), `"'`)
		if acceptable(value) {
			return value
		}
	}
	return ""
}

func acceptable(value string) bool {
	if value == "" || value == "-" || len(value) > 64 || len(strings.Fields(value)) > 3 {
		return false
	}
	lower := strings.ToLower(value)
	for _, w := range noPassword {
		if strings.Contains(lower, w) {
			return false
		}
	}
	return true
}
