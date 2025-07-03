// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testIssueQueryStringOpt struct {
	Keyword string
	Results []Token
}

var testOpts = []testIssueQueryStringOpt{
	{
		Keyword: "Hello",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "Hello World",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "Hello  World",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: " Hello World ",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "+Hello +World",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptMust,
			},
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptMust,
			},
		},
	},
	{
		Keyword: "+Hello World",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptMust,
			},
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "+Hello -World",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptMust,
			},
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptNot,
			},
		},
	},
	{
		Keyword: "\"Hello World\"",
		Results: []Token{
			{
				Term:  "Hello World",
				Fuzzy: false,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "+\"Hello World\"",
		Results: []Token{
			{
				Term:  "Hello World",
				Fuzzy: false,
				Kind:  BoolOptMust,
			},
		},
	},
	{
		Keyword: "-\"Hello World\"",
		Results: []Token{
			{
				Term:  "Hello World",
				Fuzzy: false,
				Kind:  BoolOptNot,
			},
		},
	},
	{
		Keyword: "\"+Hello -World\"",
		Results: []Token{
			{
				Term:  "+Hello -World",
				Fuzzy: false,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "\\+Hello", // \+Hello => +Hello
		Results: []Token{
			{
				Term:  "+Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "\\\\Hello", // \\Hello => \Hello
		Results: []Token{
			{
				Term:  "\\Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "\\\"Hello", // \"Hello => "Hello
		Results: []Token{
			{
				Term:  "\"Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "\\",
		Results: nil,
	},
	{
		Keyword: "\"",
		Results: nil,
	},
	{
		Keyword: "Hello \\",
		Results: []Token{
			{
				Term:  "Hello",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "\"\"",
		Results: nil,
	},
	{
		Keyword: "\" World \"",
		Results: []Token{
			{
				Term:  " World ",
				Fuzzy: false,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "\"\" World \"\"",
		Results: []Token{
			{
				Term:  "World",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
	{
		Keyword: "Best \"Hello World\" Ever",
		Results: []Token{
			{
				Term:  "Best",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
			{
				Term:  "Hello World",
				Fuzzy: false,
				Kind:  BoolOptShould,
			},
			{
				Term:  "Ever",
				Fuzzy: true,
				Kind:  BoolOptShould,
			},
		},
	},
}

func TestIssueQueryString(t *testing.T) {
	var opt SearchOptions
	for _, res := range testOpts {
		t.Run(opt.Keyword, func(t *testing.T) {
			opt.Keyword = res.Keyword
			tokens, err := opt.Tokens()
			require.NoError(t, err)
			assert.Equal(t, res.Results, tokens)
		})
	}
}

func TestToken_ParseIssueReference(t *testing.T) {
	var tk Token
	{
		tk.Term = "123"
		id, err := tk.ParseIssueReference()
		require.NoError(t, err)
		assert.Equal(t, int64(123), id)
	}
	{
		tk.Term = "#123"
		id, err := tk.ParseIssueReference()
		require.NoError(t, err)
		assert.Equal(t, int64(123), id)
	}
	{
		tk.Term = "!123"
		id, err := tk.ParseIssueReference()
		require.NoError(t, err)
		assert.Equal(t, int64(123), id)
	}
	{
		tk.Term = "text"
		_, err := tk.ParseIssueReference()
		require.Error(t, err)
	}
	{
		tk.Term = ""
		_, err := tk.ParseIssueReference()
		require.Error(t, err)
	}
}
