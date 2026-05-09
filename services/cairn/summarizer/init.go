// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"fmt"
	"os"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// Init constructs the production Service and registers it as global.
// Called from routers/init.go at startup. hmacKeyPath is the path to the
// instance HMAC key (the same path the rest of Cairn reads from — passed
// in to avoid coupling summarizer to the identity service's accessor).
func Init(engine *xorm.Engine, hmacKeyPath string) error {
	hmacKey, err := os.ReadFile(hmacKeyPath)
	if err != nil {
		return fmt.Errorf("summarizer: read hmac key: %w", err)
	}

	resolver := func(ownerID int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		cfg := &cairnmodels.SummarizerConfig{}
		has, err := engine.Where("owner_id = ?", ownerID).Get(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("summarizer: load config: %w", err)
		}
		if !has || !cfg.Enabled {
			return nil, cfg, nil
		}
		apiKey, err := DecryptCredential(hmacKey, cfg.CredentialsCipher)
		if err != nil {
			return nil, cfg, fmt.Errorf("summarizer: decrypt: %w", err)
		}
		provider, err := BuildBridleProviderFromConfig(cfg, apiKey)
		if err != nil {
			return nil, cfg, err
		}
		client, err := NewSummarizerWithProvider(provider, cfg.ModelID)
		if err != nil {
			return nil, cfg, err
		}
		return client, cfg, nil
	}

	SetGlobal(NewService(engine, resolver))
	return nil
}
