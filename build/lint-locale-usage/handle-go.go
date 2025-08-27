// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"go/ast"
	goParser "go/parser"
	"go/token"
	"strconv"
	"strings"
)

func (handler Handler) handleGoTrBasicLit(fset *token.FileSet, argLit *ast.BasicLit, prefix string) {
	if argLit.Kind == token.STRING {
		// extract string content
		arg, err := strconv.Unquote(argLit.Value)
		if err != nil {
			return
		}
		// found interesting strings
		arg = prefix + arg
		if strings.HasSuffix(arg, ".") || strings.HasSuffix(arg, "_") {
			prep, trunc := PrepareMsgidPrefix(arg)
			if trunc {
				handler.OnWarning(fset, argLit.ValuePos, fmt.Sprintf("needed to truncate message id prefix: %s", arg))
			}
			handler.OnMsgidPrefix(fset, argLit.ValuePos, prep, trunc)
		} else {
			handler.OnMsgid(fset, argLit.ValuePos, arg)
		}
	}
}

func (handler Handler) handleGoTrArgument(fset *token.FileSet, n ast.Expr, prefix string) {
	if argLit, ok := n.(*ast.BasicLit); ok {
		handler.handleGoTrBasicLit(fset, argLit, prefix)
	} else if argBinExpr, ok := n.(*ast.BinaryExpr); ok {
		if argBinExpr.Op != token.ADD {
			// pass
		} else if argLit, ok := argBinExpr.X.(*ast.BasicLit); ok && argLit.Kind == token.STRING {
			// extract string content
			arg, err := strconv.Unquote(argLit.Value)
			if err != nil {
				return
			}
			// found interesting strings
			arg = prefix + arg
			prep, trunc := PrepareMsgidPrefix(arg)
			if trunc {
				handler.OnWarning(fset, argLit.ValuePos, fmt.Sprintf("needed to truncate message id prefix: %s", arg))
			}
			handler.OnMsgidPrefix(fset, argLit.ValuePos, prep, trunc)
		}
	}
}

func (handler Handler) handleGoCommentGroup(fset *token.FileSet, cg *ast.CommentGroup, commentPrefix string) *string {
	if cg == nil {
		return nil
	}
	var matches []token.Pos
	matchInsPrefix := ""
	commentPrefix = "//" + commentPrefix
	for _, comment := range cg.List {
		ctxt := strings.TrimSpace(comment.Text)
		if ctxt == commentPrefix {
			matches = append(matches, comment.Slash)
		} else if after, found := strings.CutPrefix(ctxt, commentPrefix+"Suffix "); found {
			matches = append(matches, comment.Slash)
			matchInsPrefix = strings.TrimSpace(after)
		}
	}
	switch len(matches) {
	case 0:
		return nil
	case 1:
		return &matchInsPrefix
	default:
		handler.OnWarning(
			fset,
			matches[0],
			fmt.Sprintf("encountered multiple %s... directives, ignoring", strings.TrimSpace(commentPrefix)),
		)
		return &matchInsPrefix
	}
}

// the `Handle*File` functions follow the following calling convention:
// * `fname` is the name of the input file
// * `src` is either `nil` (then the function invokes `ReadFile` to read the file)
//   or the contents of the file as {`[]byte`, or a `string`}

func (handler Handler) HandleGoFile(fname string, src any) error {
	fset := token.NewFileSet()
	node, err := goParser.ParseFile(fset, fname, src, goParser.SkipObjectResolution|goParser.ParseComments)
	if err != nil {
		return LocatedError{
			Location: fname,
			Kind:     "Go parser",
			Err:      err,
		}
	}

	ast.Inspect(node, func(n ast.Node) bool {
		// search for function calls of the form `anything.Tr(any-string-lit, ...)`

		switch n2 := n.(type) {
		case *ast.CallExpr:
			if len(n2.Args) == 0 {
				return true
			}
			funSel, ok := n2.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			ltf, ok := handler.LocaleTrFunctions[funSel.Sel.Name]
			if !ok {
				return true
			}

			var gotUnexpectedInvoke *int

			for _, argNum := range ltf {
				if len(n2.Args) <= int(argNum) {
					argc := len(n2.Args)
					gotUnexpectedInvoke = &argc
				} else {
					handler.handleGoTrArgument(fset, n2.Args[int(argNum)], "")
				}
			}

			if gotUnexpectedInvoke != nil {
				handler.OnUnexpectedInvoke(fset, funSel.Sel.NamePos, funSel.Sel.Name, *gotUnexpectedInvoke)
			}
		case *ast.CompositeLit:
			ident, ok := n2.Type.(*ast.Ident)
			if !ok {
				return true
			}

			// special case: models/unit/unit.go
			if strings.HasSuffix(fname, "unit.go") && ident.Name == "Unit" {
				if len(n2.Elts) != 6 {
					handler.OnWarning(fset, n2.Pos(), "unexpected initialization of 'Unit' (unexpected number of arguments)")
				}
				// NameKey has index 2
				//   invoked like '{{ctx.Locale.Tr $unit.NameKey}}'
				nameKey, ok := n2.Elts[2].(*ast.BasicLit)
				if !ok || nameKey.Kind != token.STRING {
					handler.OnWarning(fset, n2.Elts[2].Pos(), "unexpected initialization of 'Unit' (expected string literal as NameKey)")
					return true
				}

				// extract string content
				arg, err := strconv.Unquote(nameKey.Value)
				if err == nil {
					// found interesting strings
					handler.OnMsgid(fset, nameKey.ValuePos, arg)
				}
			}
		case *ast.FuncDecl:
			matchInsPrefix := handler.handleGoCommentGroup(fset, n2.Doc, "llu:returnsTrKey")
			if matchInsPrefix == nil {
				return true
			}
			results := n2.Type.Results.List
			if len(results) != 1 {
				handler.OnWarning(fset, n2.Type.Func, fmt.Sprintf("function %s has unexpected return type; expected single return value", n2.Name.Name))
				return true
			}

			ast.Inspect(n2.Body, func(n ast.Node) bool {
				// search for return stmts
				// TODO: what about nested functions?
				if ret, ok := n.(*ast.ReturnStmt); ok {
					for _, res := range ret.Results {
						ast.Inspect(res, func(n ast.Node) bool {
							if expr, ok := n.(ast.Expr); ok {
								handler.handleGoTrArgument(fset, expr, *matchInsPrefix)
							}
							return true
						})
					}
					return false
				}
				return true
			})
			return true
		case *ast.GenDecl:
			if !(n2.Tok == token.CONST || n2.Tok == token.VAR) {
				return true
			}
			matchInsPrefix := handler.handleGoCommentGroup(fset, n2.Doc, " llu:TrKeys")
			if matchInsPrefix == nil {
				return true
			}
			for _, spec := range n2.Specs {
				// interpret all contained strings as message IDs
				ast.Inspect(spec, func(n ast.Node) bool {
					if argLit, ok := n.(*ast.BasicLit); ok {
						handler.handleGoTrBasicLit(fset, argLit, *matchInsPrefix)
						return false
					}
					return true
				})
			}
		}

		return true
	})

	return nil
}
