package internal

import (
	"fmt"
	"github.com/nar-lang/nar-common/ast"
	"github.com/nar-lang/nar-common/ast/normalized"
	"github.com/nar-lang/nar-common/ast/parsed"
	"github.com/nar-lang/nar-common/ast/typed"
	"github.com/nar-lang/nar-common/common"
	"github.com/nar-lang/nar-compiler"
	"github.com/nar-lang/nar-compiler/locator"
	"github.com/nar-lang/nar-lsp/internal/protocol"
	"slices"
	"strings"
	"unicode"
)

func (s *server) Initialize(params *protocol.InitializeParams) (protocol.InitializeResult, error) {
	s.locker.Lock()
	defer s.locker.Unlock()

	s.rootURI = params.RootURI
	if params.Trace != nil {
		s.trace = *params.Trace
	}
	for _, f := range params.WorkspaceFolders {
		s.workspaceProviders = append(s.workspaceProviders,
			locator.NewDirectoryProvider(uriToPath(protocol.DocumentURI(f.URI))))
	}

	return protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: true,
				Change:    protocol.Full,
			},
			DefinitionProvider: &protocol.Or_ServerCapabilities_definitionProvider{
				Value: protocol.DefinitionOptions{},
			},
			TypeDefinitionProvider: &protocol.Or_ServerCapabilities_typeDefinitionProvider{
				Value: protocol.TypeDefinitionOptions{},
			},
			ReferencesProvider: &protocol.Or_ServerCapabilities_referencesProvider{
				Value: protocol.ReferenceOptions{},
			},
			HoverProvider: &protocol.Or_ServerCapabilities_hoverProvider{
				Value: true,
			},
			DocumentSymbolProvider: &protocol.Or_ServerCapabilities_documentSymbolProvider{
				Value: protocol.DocumentSymbolOptions{},
			},
			SemanticTokensProvider: &protocol.SemanticTokensOptions{
				Legend: protocol.SemanticTokensLegend{
					TokenTypes:     ast.SemanticTokenTypesLegend,
					TokenModifiers: ast.SemanticTokenModifiersLegend,
				},
				Range: &protocol.Or_SemanticTokensOptions_range{
					Value: false,
				},
				Full: &protocol.Or_SemanticTokensOptions_full{
					Value: protocol.PFullESemanticTokensOptions{
						Delta: false,
					},
				},
			},
			CompletionProvider: &protocol.CompletionOptions{
				TriggerCharacters: []string{"."},
			},
			SignatureHelpProvider: &protocol.SignatureHelpOptions{
				TriggerCharacters:   []string{"("},
				RetriggerCharacters: []string{","},
			},
			RenameProvider: &protocol.RenameOptions{},
		},
		ServerInfo: &protocol.PServerInfoMsg_initialize{
			Name:    "Nar Language Server",
			Version: fmt.Sprintf("%d.%02d", Version/100, Version%100),
		},
	}, nil
}

func (s *server) Initialized(_ *nothing) error {
	s.initialized = true
	return nil
}

func (s *server) Shutdown(_ *nothing) error {
	s.initialized = false
	return nil
}

func (s *server) S_setTraceNotification(params *protocol.SetTraceParams) error {
	s.trace = params.Value
	return nil
}

func (s *server) TextDocument_didOpen(params *protocol.DidOpenTextDocumentParams) error {
	s.setDocumentStatus(params.TextDocument.URI, true)
	if pvd, ok := s.getProvider(params.TextDocument.URI); ok {
		pvd.OverrideFile(params.TextDocument.URI, []rune(params.TextDocument.Text))
		s.compileChan <- docChange{uri: params.TextDocument.URI, force: true}
	}
	return nil
}

func (s *server) TextDocument_didChange(params *protocol.DidChangeTextDocumentParams) error {
	if pvd, ok := s.getProvider(params.TextDocument.URI); ok {
		pvd.OverrideFile(params.TextDocument.URI, []rune(params.ContentChanges[0].Text))
		s.compileChan <- docChange{uri: params.TextDocument.URI, force: false}
	}
	return nil

}

func (s *server) TextDocument_didClose(params *protocol.DidCloseTextDocumentParams) error {
	if pvd, ok := s.getProvider(params.TextDocument.URI); ok {
		pvd.OverrideFile(params.TextDocument.URI, nil)
	}
	s.setDocumentStatus(params.TextDocument.URI, false)
	return nil
}

func (s *server) TextDocument_definition(
	params *protocol.DefinitionParams,
) (result *protocol.Location, err error) {
	if loc, m, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character); ok {
		_, _, stmt := s.statementAtLocation(loc, m)
		switch stmt.(type) {
		case *typed.Global:
			def := stmt.(*typed.Global).Definition()
			if def != nil {
				return locToLocation(def.Location()), nil
			}
		case *typed.Local:
			target := stmt.(*typed.Local).Target()
			if target != nil {
				return locToLocation(target.Location()), nil
			}
		case typed.Type:
			return locToLocation(stmt.Location()), nil
		case *typed.POption:
			def := stmt.(*typed.POption).Definition()
			if def != nil {
				return locToLocation(def.Location()), nil
			}
		}
	}
	return nil, nil
}

func (s *server) TextDocument_typeDefinition(
	params *protocol.TypeDefinitionParams,
) (result *protocol.Location, err error) {
	if loc, m, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character); ok {
		_, _, stmt := s.statementAtLocation(loc, m)
		switch stmt.(type) {
		case *typed.Global:
			t := stmt.(*typed.Global).Type()
			if t != nil {
				return locToLocation(t.Location()), nil
			}
		case *typed.Local:
			t := stmt.(*typed.Local).Type()
			if t != nil && t.Location().FilePath() != "" {
				return locToLocation(t.Location()), nil
			}
		case typed.Pattern:
			t := stmt.(typed.Pattern).Type()
			if t != nil {
				return locToLocation(t.Location()), nil
			}
		}
	}
	return
}

func (s *server) TextDocument_references(
	params *protocol.ReferenceParams,
) (result []protocol.Location, err error) {
	if loc, m, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character); ok {
		_, _, stmt := s.statementAtLocation(loc, m)

		appendDefinition := func(def *typed.Definition) {
			result = append(result, *locToLocation(def.NameLocation()))
			for _, m := range s.parsedModules {
				m.Iterate(func(e parsed.Statement) {
					nStmt := e.Successor()
					if nStmt != nil {
						if g, ok := nStmt.Successor().(*typed.Global); ok {
							if g.Definition().Id() == def.Id() {
								result = append(result, *locToLocation(e.Location()))
							}
						}
					}
				})
			}
		}

		appendPattern := func(pattern typed.Pattern) {
			result = append(result, *locToLocation(pattern.Location()))
			for _, m := range s.parsedModules {
				if pattern.Location().FilePath() == m.Location().FilePath() {
					m.Iterate(func(stmt parsed.Statement) {
						nStmt := stmt.Successor()
						if nStmt != nil {
							tStmt := nStmt.Successor()
							if l, ok := tStmt.(*typed.Local); ok {
								if l.Target() == pattern {
									result = append(result, *locToLocation(stmt.Location()))
								}
							}
						}
					})
				}
			}
		}

		switch stmt.(type) {
		case *typed.Global:
			def := stmt.(*typed.Global).Definition()
			if def != nil {
				appendDefinition(def)
			}
			break
		case *typed.Local:
			target := stmt.(*typed.Local).Target()
			if target != nil {
				appendPattern(target)
			}
		case *typed.Definition:
			def := stmt.(*typed.Definition)
			appendDefinition(def)
			break
		case typed.Type:
			tNative, isNative := stmt.(*typed.TNative)
			tData, isData := stmt.(*typed.TData)
			if isNative || isData {
				for _, m := range s.parsedModules {
					m.Iterate(func(e parsed.Statement) {
						nStmt := e.Successor()
						if nStmt != nil {
							xNative, xIsNative := nStmt.Successor().(*typed.TNative)
							xData, xIsData := nStmt.Successor().(*typed.TData)
							if xIsNative && isNative && xNative.Name() == tNative.Name() {
								result = append(result, *locToLocation(e.Location()))
							}
							if xIsData && isData && xData.Name() == tData.Name() {
								result = append(result, *locToLocation(e.Location()))
							}
						}
					})
				}
			}
			break
		case typed.Pattern:
			pattern := stmt.(typed.Pattern)
			appendPattern(pattern)
			break
		}
	}

	return
}

func (s *server) TextDocument_hover(params *protocol.HoverParams) (*protocol.Hover, error) {
	if loc, mod, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character); ok {
		_, _, stmt := s.statementAtLocation(loc, mod)
		if stmt != nil {
			return &protocol.Hover{
				Contents: protocol.MarkupContent{
					Kind:  protocol.Markdown,
					Value: stmt.Code(mod.Name()), //TODO: make it useful
				},
				Range: locToRange(stmt.Location()),
			}, nil
		}
	}
	return nil, nil
}

func (s *server) TextDocument_documentSymbol(
	params *protocol.DocumentSymbolParams,
) (result []protocol.DocumentSymbol, err error) {
	path := uriToPath(params.TextDocument.URI)
	for _, mod := range s.parsedModules {
		if mod.Location().FilePath() == path {
			for _, inf := range mod.InfixFns() {
				result = append(result, protocol.DocumentSymbol{
					Name:           string(inf.Name()),
					Kind:           protocol.Operator,
					Range:          locToRange(inf.Location()),
					SelectionRange: locToRange(inf.Location()),
				})
			}
			for _, alias := range mod.Aliases() {
				findDT := func(x parsed.DataType) bool { return alias.Name() == x.Name() }
				if _, ok := common.Find(findDT, mod.DataTypes()); ok {
					continue
				}

				kind := protocol.Class
				var children []protocol.DocumentSymbol
				nType := alias.Successor()
				if nType != nil {
					tType := nType.Successor()
					switch tType.(type) {
					case nil:
						break
					case *typed.TRecord:
						kind = protocol.Struct
						for name, f := range tType.(*typed.TRecord).Fields() {
							children = append(children, protocol.DocumentSymbol{
								Name:           string(name),
								Kind:           protocol.Field,
								Range:          locToRange(f.Location()),
								SelectionRange: locToRange(f.Location()),
							})
						}

					case *typed.TFunc:
						kind = protocol.Function
					case *typed.TTuple:
						kind = protocol.Array
					case *typed.TNative:
						kind = protocol.Class
					case *typed.TUnbound:
						kind = protocol.Null

					}
					result = append(result, protocol.DocumentSymbol{
						Name:           string(alias.Name()),
						Kind:           kind,
						Range:          locToRange(alias.Location()),
						SelectionRange: locToRange(alias.Location()),
						Children:       children,
					})
				}
			}
			for _, dt := range mod.DataTypes() {
				result = append(result, protocol.DocumentSymbol{
					Name:           string(dt.Name()),
					Kind:           protocol.Enum,
					Range:          locToRange(dt.Location()),
					SelectionRange: locToRange(dt.Location()),
					Children: common.Map(func(o parsed.DataTypeOption) protocol.DocumentSymbol {
						return protocol.DocumentSymbol{
							Name:           string(o.Name()),
							Kind:           protocol.EnumMember,
							Range:          locToRange(o.Location()),
							SelectionRange: locToRange(o.Location()),
						}
					}, dt.Options()),
				})
			}
			for _, d := range mod.Definitions() {
				if unicode.IsLower([]rune(d.Name())[0]) {
					kind := protocol.Function
					if len(d.Params()) == 0 {
						kind = protocol.Constant
					}
					if _, ok := d.Body().(*parsed.Call); ok {
						kind = protocol.Interface
					}
					result = append(result, protocol.DocumentSymbol{
						Name:           string(d.Name()),
						Kind:           kind,
						Range:          locToRange(d.Location()),
						SelectionRange: locToRange(d.Location()),
					})
				}
			}
			break
		}
	}
	return
}

func (s *server) TextDocument_semanticTokens_full(
	params *protocol.SemanticTokensParams,
) (*protocol.SemanticTokens, error) {
	if len(s.parsedModules) == 0 {
		<-s.compiledChan
	}
	path := uriToPath(params.TextDocument.URI)
	for _, mod := range s.parsedModules {
		if mod.Location().FilePath() == path {
			var tokens []ast.SemanticToken
			mod.Iterate(func(stmt parsed.Statement) {
				tokens = append(tokens, stmt.SemanticTokens()...)
			})
			slices.SortFunc(tokens, func(a, b ast.SemanticToken) int {
				if a.Line < b.Line {
					return -1
				}
				if a.Line > b.Line {
					return 1
				}
				if a.Char < b.Char {
					return -1
				}
				if a.Char > b.Char {
					return 1
				}
				return 0
			})

			deltas := make([]ast.SemanticToken, 0, len(tokens))
			deltas = append(deltas, tokens[0])
			for i := 1; i < len(tokens); i++ {
				p := tokens[i-1]
				t := tokens[i]
				dl := t.Line - p.Line
				if dl == 0 {
					t.Line = 0
					t.Char = t.Char - p.Char
				} else {
					t.Line = dl
				}
				deltas = append(deltas, t)
			}

			return &protocol.SemanticTokens{
				Data: common.Fold(
					func(t ast.SemanticToken, acc []uint32) []uint32 {
						return append(acc, t.Line, t.Char, t.Length, uint32(t.Type), uint32(t.Modifiers))
					}, nil, deltas),
			}, nil
		}
	}
	return nil, nil
}

var keywordCompletions []protocol.CompletionItem

func init() {
	keywordCompletions = common.Map(func(k string) protocol.CompletionItem {
		return protocol.CompletionItem{
			Label: k,
			Kind:  protocol.KeywordCompletion,
		}
	}, nar_compiler.Keywords)
}

func (s *server) TextDocument_completion(
	params *protocol.CompletionParams,
) (*protocol.CompletionList, error) {
	localItems := map[ast.Identifier]struct{}{}
	var appendLocals func(locals ...normalized.Pattern)
	appendLocals = func(locals ...normalized.Pattern) {
		for _, p := range locals {
			switch p.(type) {
			case *normalized.PAlias:
				localItems[p.(*normalized.PAlias).Alias()] = struct{}{}
				appendLocals(p.(*normalized.PAlias).Nested())
			case *normalized.PCons:
				appendLocals(p.(*normalized.PCons).Head(), p.(*normalized.PCons).Tail())
			case *normalized.PList:
				appendLocals(p.(*normalized.PList).Items()...)
			case *normalized.PNamed:
				localItems[p.(*normalized.PNamed).Name()] = struct{}{}
			case *normalized.POption:
				appendLocals(p.(*normalized.POption).Values()...)
			case *normalized.PRecord:
				for _, f := range p.(*normalized.PRecord).Fields() {
					localItems[f.Name()] = struct{}{}
				}
			case *normalized.PTuple:
				appendLocals(p.(*normalized.PTuple).Items()...)
			}
		}
	}

	loc, module, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character)
	if ok {
		module.Iterate(func(stmt parsed.Statement) {
			if stmt.Location().Contains(loc) {
				nStmt := stmt.Successor()
				switch nStmt.(type) {
				case normalized.Definition:
					appendLocals(nStmt.(normalized.Definition).Params()...)
				case *normalized.Let:
					appendLocals(nStmt.(*normalized.Let).Pattern())
				case *normalized.Select:
					for _, cs := range nStmt.(*normalized.Select).Cases() {
						if cs.Location().Contains(loc) {
							appendLocals(cs.Pattern())
						}
					}
				}
			}
		})
	}

	var completions []protocol.CompletionItem
	for _, m := range s.parsedModules {
		if m != nil {
			fullName := m.Name()
			shortName := ast.QualifiedIdentifier("")
			alias := ast.Identifier("")

			for _, imp := range module.Imports() {
				if imp.Module() == m.Name() {
					if imp.Alias() != nil {
						alias = *imp.Alias()
					}
				}
			}

			isCurrentModule := m == module
			lastDotIndex := strings.LastIndex(string(fullName), ".")
			if lastDotIndex >= 0 {
				shortName = fullName[lastDotIndex+1:]
			}

			addName := func(name ast.Identifier, kind protocol.CompletionItemKind) {
				if isCurrentModule {
					completions = append(completions, protocol.CompletionItem{
						Label: string(name),
						Kind:  kind,
					})
				} else {
					completions = append(completions,
						protocol.CompletionItem{
							Label: fmt.Sprintf("%s.%s", fullName, name),
							Kind:  kind,
						})
					if alias != "" {
						completions = append(completions,
							protocol.CompletionItem{
								Label: fmt.Sprintf("%s.%s", alias, name),
								Kind:  kind,
							})
					} else if shortName != "" {
						completions = append(completions,
							protocol.CompletionItem{
								Label: fmt.Sprintf("%s.%s", shortName, name),
								Kind:  kind,
							})
					}

				}
			}

			for _, def := range m.Definitions() {
				if isCurrentModule || !def.Hidden() {
					kind := protocol.FunctionCompletion
					if len(def.Params()) == 0 {
						kind = protocol.ConstantCompletion
					}
					addName(def.Name(), kind)
				}
			}
			for _, alias := range m.Aliases() {
				if isCurrentModule || !alias.Hidden() {
					addName(alias.Name(), protocol.ClassCompletion)
				}
			}
			for _, dt := range m.DataTypes() {
				if isCurrentModule || !dt.Hidden() {
					addName(dt.Name(), protocol.EnumCompletion)
					for _, opt := range dt.Options() {
						if isCurrentModule || !opt.Hidden() {
							addName(opt.Name(), protocol.EnumMemberCompletion)
						}
					}
				}
			}
			for _, ifx := range m.InfixFns() {
				completions = append(completions,
					protocol.CompletionItem{
						Label: string(ifx.Name()),
						Kind:  protocol.OperatorCompletion,
					})
			}
		}
	}

	list := append(
		common.Map(func(i ast.Identifier) protocol.CompletionItem {
			return protocol.CompletionItem{
				Label: string(i),
				Kind:  protocol.VariableCompletion,
			}
		}, common.Keys(localItems)),
		completions...)
	list = append(list, keywordCompletions...)

	return &protocol.CompletionList{IsIncomplete: false, Items: list}, nil
}

func (s *server) TextDocument_signatureHelp(
	params *protocol.SignatureHelpParams,
) (*protocol.SignatureHelp, error) {

	extractSignature := func(loc ast.Location) *protocol.SignatureHelp {
		str := loc.Text()
		eq := strings.Index(str, "=")
		if eq >= 0 {
			str = str[:eq]
		}
		return &protocol.SignatureHelp{
			Signatures: []protocol.SignatureInformation{
				{
					Label: str,
				},
			},
		}
	}

	extractSignatureFromDef := func(modName ast.QualifiedIdentifier, defName ast.Identifier) *protocol.SignatureHelp {
		if defMod, ok := s.parsedModules[modName]; ok {
			def, ok := common.Find(func(d parsed.Definition) bool { return d.Name() == defName }, defMod.Definitions())
			if ok {
				return extractSignature(def.Location())
			}
		}
		return nil
	}

	var signature *protocol.SignatureHelp
	if loc, m, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character); ok {
		m.Iterate(func(stmt parsed.Statement) {
			if stmt.Location().Contains(loc) {
				nStmt := stmt.Successor()
				switch nStmt.(type) {
				case *normalized.Apply:
					apply := nStmt.(*normalized.Apply)
					switch apply.Func().(type) {
					case *normalized.Global:
						signature = extractSignatureFromDef(apply.Func().(*normalized.Global).DefinitionName())
					case *normalized.Local:
						local := apply.Func().(*normalized.Local).Target()
						signature = extractSignature(local.Location())
					}
				case *normalized.POption:
					signature = extractSignatureFromDef(nStmt.(*normalized.POption).DefinitionName())
				}
			}
		})
	}
	return signature, nil
}

func (s *server) TextDocument_rename(
	params *protocol.RenameParams,
) (*protocol.WorkspaceEdit, error) {
	loc, mod, ok := s.locationUnderCursor(params.TextDocument.URI, params.Position.Line, params.Position.Character)
	result := &protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentURI][]protocol.TextEdit{},
	}
	appendDefinition := func(def *typed.Definition) {
		if def != nil {
			result.Changes[pathToUri(def.Location().FilePath())] = append(
				result.Changes[pathToUri(def.Location().FilePath())],
				protocol.TextEdit{
					Range:   locToRange(def.NameLocation()),
					NewText: params.NewName,
				})
			for _, m := range s.parsedModules {
				m.Iterate(func(e parsed.Statement) {
					nStmt := e.Successor()
					if nStmt != nil {
						if g, ok := nStmt.Successor().(*typed.Global); ok {
							if g.Definition().Id() == def.Id() {
								result.Changes[pathToUri(def.Location().FilePath())] = append(
									result.Changes[pathToUri(def.Location().FilePath())],
									protocol.TextEdit{
										Range:   locToRange(e.Location()),
										NewText: params.NewName,
									})
							}
						}
					}
				})
			}
		}
	}
	appendPattern := func(pattern typed.Pattern) {
		if pattern != nil {
			result.Changes[pathToUri(pattern.Location().FilePath())] = append(
				result.Changes[pathToUri(pattern.Location().FilePath())],
				protocol.TextEdit{
					Range:   locToRange(pattern.Location()),
					NewText: params.NewName,
				})
			for _, m := range s.parsedModules {
				if pattern.Location().FilePath() == m.Location().FilePath() {
					m.Iterate(func(stmt parsed.Statement) {
						nStmt := stmt.Successor()
						if nStmt != nil {
							tStmt := nStmt.Successor()
							if l, ok := tStmt.(*typed.Local); ok {
								if l.Target() == pattern {
									result.Changes[pathToUri(pattern.Location().FilePath())] = append(
										result.Changes[pathToUri(pattern.Location().FilePath())],
										protocol.TextEdit{
											Range:   locToRange(stmt.Location()),
											NewText: params.NewName,
										})
								}
							}
						}
					})
				}
			}
		}
	}

	if ok {
		_, _, stmt := s.statementAtLocation(loc, mod)
		if stmt != nil {
			switch stmt.(type) {
			case *typed.Global:
				def := stmt.(*typed.Global).Definition()
				if def != nil {
					appendDefinition(def)
				}
				break
			case *typed.Local:
				target := stmt.(*typed.Local).Target()
				if target != nil {
					appendPattern(target)
				}
			case *typed.Definition:
				def := stmt.(*typed.Definition)
				appendDefinition(def)
				break
			case typed.Pattern:
				pattern := stmt.(typed.Pattern)
				appendPattern(pattern)
				break
			}
		}
	}
	return result, nil
}
