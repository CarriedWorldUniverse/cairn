// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/services/notify"
)

// hmacKeyForEncrypt caches the HMAC key bytes loaded by Init so the API
// handlers can encrypt new credentials without re-reading from disk on
// every PUT. atomic.Pointer keeps the read lock-free; SetGlobal-style
// reload would re-store here.
var hmacKeyForEncrypt atomic.Pointer[[]byte]

// HMACKey returns the bytes loaded by Init, or nil if Init hasn't run.
// API handlers use this to keep the encrypt path consistent with the
// init-time decrypt path (single source of truth for the key, no disk
// I/O on the hot path, no rotation asymmetry).
func HMACKey() []byte {
	p := hmacKeyForEncrypt.Load()
	if p == nil {
		return nil
	}
	return *p
}

// Init constructs the production Service and registers it as global.
// Called from routers/init.go at startup. hmacKeyPath is the path to the
// instance HMAC key (the same path the rest of Cairn reads from — passed
// in to avoid coupling summarizer to the identity service's accessor).
func Init(engine *xorm.Engine, hmacKeyPath string) error {
	hmacKey, err := os.ReadFile(hmacKeyPath)
	if err != nil {
		return fmt.Errorf("summarizer: read hmac key: %w", err)
	}
	keyCopy := append([]byte(nil), hmacKey...)
	hmacKeyForEncrypt.Store(&keyCopy)

	resolver := func(ownerID int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		cfg := &cairnmodels.SummarizerConfig{}
		has, err := engine.Where("owner_id = ?", ownerID).Get(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("summarizer: load config: %w", err)
		}
		if !has {
			return nil, nil, nil
		}
		if !cfg.Enabled {
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

	q := newQueue(Global(), 5*time.Second)
	notify.RegisterNotifier(&prNotifier{queue: q})
	return nil
}
