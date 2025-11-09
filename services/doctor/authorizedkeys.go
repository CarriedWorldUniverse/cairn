// Copyright 2020 The Gitea Authors. All rights reserved.
// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package doctor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
)

func checkAuthorizedKeys(ctx context.Context, logger log.Logger, autofix bool) error {
	if setting.SSH.StartBuiltinServer || !setting.SSH.CreateAuthorizedKeysFile {
		return nil
	}

	findings, err := asymkey_model.InspectPublicKeys(ctx)
	if err != nil {
		return fmt.Errorf("inspect authorized_keys failed: %w", err)
	}

	if !autofix {
		for _, finding := range findings {
			switch finding.Type {
			case asymkey_model.InspectionResultFileMissing:
				logger.Critical("authorized_keys file is missing")
			case asymkey_model.InspectionResultUnexpectedKey:
				if !setting.SSH.AllowUnexpectedAuthorizedKeys {
					logger.Critical(finding.Comment)
				}
			case asymkey_model.InspectionResultMissingExpectedKey:
				logger.Critical(finding.Comment)
			}
		}
	}

	if len(findings) > 0 {
		if !autofix {
			fPath := filepath.Join(setting.SSH.RootPath, "authorized_keys")
			logger.Critical(
				"authorized_keys file %q contains validity errors.\nRegenerate it with:\n\t\"%s\"\nor\n\t\"%s\"",
				fPath,
				"forgejo admin regenerate keys",
				"forgejo doctor check --run authorized-keys --fix")
			return errors.New("errors discovered from InspectPublicKeys")
		}
		err := asymkey_model.RewriteAllPublicKeys(ctx)
		if err != nil {
			return fmt.Errorf("rewrite authorized_keys failed: %w", err)
		}
	}

	return nil
}

func init() {
	Register(&Check{
		Title:     "Check if OpenSSH authorized_keys file is up-to-date",
		Name:      "authorized-keys",
		IsDefault: true,
		Run:       checkAuthorizedKeys,
		Priority:  4,
	})
}
