package internal

import (
	"github.com/nar-lang/nar-common/ast"
	"github.com/nar-lang/nar-common/ast/normalized"
	"github.com/nar-lang/nar-common/ast/parsed"
	"github.com/nar-lang/nar-common/ast/typed"
	locator2 "github.com/nar-lang/nar-compiler/locator"
	"github.com/nar-lang/nar-lsp/internal/protocol"
	"os"
	"path/filepath"
)

func (s *server) locationUnderCursor(docURI protocol.DocumentURI, line, char uint32) (ast.Location, *parsed.Module, bool) {
	path := uriToPath(docURI)
	for _, m := range s.parsedModules {
		if m != nil && m.Location().FilePath() == path {
			loc := ast.NewLocationSrc(path, m.Location().FileContent(), line, char)
			return loc, m, true
		}
	}
	return ast.Location{}, nil, false
}

func (s *server) statementAtLocation(
	loc ast.Location, m *parsed.Module,
) (
	parsed.Statement, normalized.Statement, typed.Statement,
) {
	var pStmt parsed.Statement
	m.Iterate(func(x parsed.Statement) {
		if x != nil && x.Location().Contains(loc) && (pStmt == nil || pStmt.Location().Size() > x.Location().Size()) {
			pStmt = x
		}
	})
	if pStmt != nil {
		nStmt := pStmt.Successor()
		var tStmt typed.Statement
		if nStmt != nil {
			tStmt = nStmt.Successor()
		}
		return pStmt, nStmt, tStmt
	}
	return nil, nil, nil
}

func (s *server) setDocumentStatus(uri protocol.DocumentURI, opened bool) {
	s.locker.Lock()
	if opened {
		s.openedDocuments[uri] = struct{}{}
	} else {
		delete(s.openedDocuments, uri)
	}

	for documentURI := range s.documentToPackageRoot {
		delete(s.documentToPackageRoot, documentURI)
	}

	packagePaths := map[string]struct{}{}
	for docURI := range s.openedDocuments {
		path := findPackageRoot(uriToPath(docURI))
		if path != "" {
			packagePaths[path] = struct{}{}
			s.documentToPackageRoot[docURI] = path
		}
	}

	for pkgPath := range packagePaths {
		if _, ok := s.provides[pkgPath]; !ok {
			s.provides[pkgPath] = newProvider(pkgPath)
		}
	}

	var providers []locator2.Provider
	for _, p := range s.provides {
		providers = append(providers, p)
	}
	providers = append(providers, s.workspaceProviders...)
	providers = append(providers, s.cacheProvider)
	s.locator = locator2.NewLocator(providers...)

	for pkgRoot := range s.packageRootToName {
		delete(s.packageRootToName, pkgRoot)
	}

	for pkgPath := range packagePaths {
		pvd, ok := s.provides[pkgPath]
		if ok {
			packages, _ := pvd.ExportedPackages()
			if len(packages) == 1 {
				s.packageRootToName[pkgPath] = ast.PackageIdentifier(packages[0].Info().Name)
			}
		}
	}
	s.locker.Unlock()
}

func (s *server) getProvider(textDocumentUrl protocol.DocumentURI) (*provider, bool) {
	p, ok := s.provides[s.documentToPackageRoot[textDocumentUrl]]
	return p, ok
}

func findPackageRoot(path string) string {
	for path != "." && path != "/" {
		path = filepath.Dir(path)
		if _, err := os.Stat(filepath.Join(path, "nar.json")); !os.IsNotExist(err) {
			return path
		}
	}
	return ""
}
