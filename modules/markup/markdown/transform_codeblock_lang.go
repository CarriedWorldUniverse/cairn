// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package markdown

import (
	"bytes"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func (g *ASTTransformer) transformCodeblockLanguage(v *ast.FencedCodeBlock, reader text.Reader) {
	src := reader.Source()
	info := v.Info.Segment.Value(src)
	// Strip language after commas
	//
	// For example,
	// ```rust,ignore
	// ...
	// ```
	// Should have a language of "rust", not "rust,ignore"
	if i := bytes.IndexByte(info, ','); i != -1 {
		start := v.Info.Segment.Start
		v.Info = ast.NewTextSegment(text.NewSegment(start, start+i))
	}
}
