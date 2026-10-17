package spotnet

import (
	"testing"
	"time"

	"github.com/spottr/spottr/internal/nntp"
)

func TestLooksLikeModerationCommand(t *testing.T) {
	// Real-world shape: "DISPOSE <msgid> - <original title>".
	cmd, ok := LooksLikeModerationCommand("DISPOSE SmUHfKk5wZAPerwaQ9Lp5@spot.net - Suske en Wiske -- nr. 000 - 386 (CBR)")
	if !ok {
		t.Fatal("real dispose title not recognised")
	}
	if cmd.Action != "dispose" {
		t.Errorf("Action = %q, want dispose", cmd.Action)
	}
	if cmd.TargetID != "<SmUHfKk5wZAPerwaQ9Lp5@spot.net>" {
		t.Errorf("TargetID = %q", cmd.TargetID)
	}

	for _, action := range []string{"delete", "dispose", "remove", "DELETE", "Dispose"} {
		if _, ok := LooksLikeModerationCommand(action + " a@b - title"); !ok {
			t.Errorf("action %q not recognised", action)
		}
	}
}

// Ordinary spots must not be mistaken for control messages — a false positive
// here silently removes real content from the index.
func TestLooksLikeModerationCommandIgnoresOrdinaryTitles(t *testing.T) {
	for _, title := range []string{
		"Deleted Scenes 2025 1080p",
		"Suske en Wiske -- nr. 000 - 386 (CBR)",
		"dispose",             // no target
		"",                    // empty
		"removed <a@b>",       // not an exact action word
		"please delete <a@b>", // action must come first
		"delete this file",    // target is not a message-id
		"remove all the duplicates",
	} {
		if cmd, ok := LooksLikeModerationCommand(title); ok {
			t.Errorf("ordinary title %q misread as command %+v", title, cmd)
		}
	}
}

func TestValidateDisposeRequiresVerification(t *testing.T) {
	now := time.Now()
	unverified := ModerationCommand{
		Action: "dispose", TargetID: "<a@b>", Spotter: "aaaaaa", Verified: false, Stamp: now,
	}
	if ValidateDispose(unverified, "aaaaaa", now.Add(-time.Hour)) {
		t.Error("unverified command passed validation")
	}

	noSpotter := ModerationCommand{
		Action: "dispose", TargetID: "<a@b>", Spotter: "", Verified: true, Stamp: now,
	}
	if ValidateDispose(noSpotter, "aaaaaa", now.Add(-time.Hour)) {
		t.Error("command with no signer identity passed validation")
	}
}

func TestValidateDisposeTimeWindow(t *testing.T) {
	now := time.Now()
	cmd := ModerationCommand{
		Action: "dispose", TargetID: "<a@b>", Spotter: "aaaaaa", Verified: true, Stamp: now,
	}

	if !ValidateDispose(cmd, "aaaaaa", now.Add(-disposeMaxAge+time.Minute)) {
		t.Error("withdrawal just inside the window was rejected")
	}
	if ValidateDispose(cmd, "aaaaaa", now.Add(-disposeMaxAge-time.Minute)) {
		t.Error("withdrawal just outside the window was accepted")
	}
	// A withdrawal predating the spot it targets is nonsense.
	if ValidateDispose(cmd, "aaaaaa", now.Add(time.Hour)) {
		t.Error("withdrawal older than its target was accepted")
	}
}

// A real moderator command (free.pt article 2254191, 2015-02-02): a moderator bot withdrawing a
// mislabelled spot, signed with the network's moderator key, no key of its own.
func TestParseDisposeRealModeratorCommand(t *testing.T) {
	ai := nntp.ArticleInfo{
		Subject:   "DISPOSE XHJY1UM6WWs7p3PVAQwki@spot.net - Fantasy Characters Attack 3D-Hentai",
		From:      "BenBotSpecial1 <10@12a01.1.10.1422900434.pEI.mod.P9iIrQFAr4FwLGvqZ7NG25zyO-scU-pFLmw8rmEteCndLwh6tfuUBDwlVbbq7DCnmV>",
		MessageID: "<mpOhU8gwxNwxrzPVAPrnf@spot.net>",
		Date:      time.Date(2015, 2, 2, 18, 7, 14, 0, time.UTC),
	}
	parse := func(ai nntp.ArticleInfo) ModerationCommand {
		spot, _, err := ParseFromOverview(ai)
		if err != nil {
			t.Fatal(err)
		}
		cmd, ok := ParseDispose(spot.Title, spot.HeaderPart, spot.Poster, spot.HeaderSig, spot.SelfSignedKey, spot.MessageID, spot.KeyID, spot.PostedAt)
		if !ok {
			t.Fatal("not recognised as a withdrawal")
		}
		return cmd
	}

	cmd := parse(ai)
	if !cmd.Verified || !cmd.Moderator || cmd.TargetID != "<XHJY1UM6WWs7p3PVAQwki@spot.net>" {
		t.Fatalf("got %+v, want a verified moderator command for <XHJY1UM6WWs7p3PVAQwki@spot.net>", cmd)
	}
	// It applies to a spot with an unknown owner, years later.
	if !ValidateDispose(cmd, "", ai.Date.AddDate(-3, 0, 0)) {
		t.Error("a verified moderator command must apply to any spot")
	}

	// Pointed at another spot, the signature no longer matches.
	forged := ai
	forged.Subject = "DISPOSE Someone3lseSpot00000@spot.net - Fantasy Characters Attack 3D-Hentai"
	if c := parse(forged); c.Verified || ValidateDispose(c, "", ai.Date) {
		t.Error("a moderator command for another target verified")
	}
}
