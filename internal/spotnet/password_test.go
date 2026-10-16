package spotnet

import "testing"

// The cases are taken from real spot descriptions (2026-10-15), plus a few edge cases.
func TestPasswordHint(t *testing.T) {
	cases := []struct{ name, description, want string }{
		{"bold label", `(SRT instelbaar)[br]Taal: Nederlands + engels[br][br][br][b]Wachtwoord[/b]: KKKK2[br][br][br][b]Heb je spotnet Classic?[/b][br]Dan kun je eenvoudig het wachtwoord als vast toevoegen.[br]Ga naar "Bewerken" -> SABnzbd -> Bewerken SABnzbd pasword[br][br][b]Wachtwoordgebruik bij SABnzbd extern[/b][br]Maak op een  gewenste plek op je pc een txt.bestand en noem dat bv: Wachtwoord SAB`, "KKKK2"},
		{"geen nodig", `Bestandsnaam: mdvfcbgnhmjyuy6t5rdf[br]Wachtwoord: Geen wachtwoord nodig[br][br]Even wat info:`, ""},
		{"niet nodig", `Bestand : cbr[br]Bestandsnaam : zxcvfgbfhujygutyrt5esdf[br]Wachtwoord : Niet nodig[br][br]`, ""},
		{"zonder", `Ondertitels: ja.[br]Wachtwoord: deze download is zonder wachtwoord geplaatst.[br][br][i]Veel kijk plezier ! [/i]`, ""},
		{"benodigd, no colon", `[br][b]--> Geen wachtwoord benodigd [/b][/i][br]Gebruik gewoon Acrobat-Reader`, ""},
		{"english", "Some text\nPassword: s3cr3t!\nMore", "s3cr3t!"},
		{"quoted", `[br]Password = "abc def"[br]`, "abc def"},
		{"pw short label", `[br]PW: x9y8[br]`, "x9y8"},
		{"none", `Password: none`, ""},
		{"dash", `Wachtwoord: -`, ""},
		{"no mention", `Titel: Sherlock Jr.[br]Jaar: 1924`, ""},
		{"word only starts with label", `Passend: ja[br]Wachtwoordgebruik: zie hieronder`, ""},
	}
	for _, c := range cases {
		if got := PasswordHint(c.description); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
