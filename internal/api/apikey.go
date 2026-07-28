package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
)

// SettingAPIKey is the app_settings row holding the newznab API key.
const SettingAPIKey = "newznab_api_key"

// ResolveAPIKey returns the effective newznab API key and whether it was newly
// generated.
//
// The key used to be generated with crypto/rand into a config field on every
// boot and never written anywhere — so it changed on each restart AND each
// container recreation, silently breaking every configured Prowlarr/Sonarr
// indexer, and could not even be read back because nothing logged it.
//
// Ownership now sits with the database, which lives on the mounted volume and
// therefore survives both. Precedence:
//
//	API_KEY env set  → use it verbatim, do not persist (supports secret managers
//	                   and declarative deployments; the env is the source of truth)
//	otherwise        → read from app_settings, generating and storing one on
//	                   first run
//
// Rotation is an explicit act (clear the row or change the env), never a side
// effect of a restart.
func ResolveAPIKey(cfg *config.Config, database *db.DB) (key string, generated bool, err error) {
	if cfg.APIKey != "" {
		return cfg.APIKey, false, nil
	}

	stored, err := database.Setting(SettingAPIKey)
	if err != nil {
		return "", false, fmt.Errorf("read api key: %w", err)
	}
	if stored != "" {
		return stored, false, nil
	}

	generated_, err := randomHex(16)
	if err != nil {
		return "", false, err
	}
	if err := database.SetSetting(SettingAPIKey, generated_); err != nil {
		return "", false, fmt.Errorf("persist api key: %w", err)
	}
	return generated_, true, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
