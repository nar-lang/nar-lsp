package internal

import (
	"fmt"
	locator2 "github.com/nar-lang/nar-compiler/locator"
	"github.com/nar-lang/nar-lsp/internal/protocol"
	"maps"
)

func newProvider(path string) *provider {
	return &provider{
		fsProvider: locator2.NewFileSystemPackageProvider(path),
		overrides:  map[string][]rune{},
		merged:     map[string][]rune{},
		path:       path,
	}
}

type provider struct {
	fsProvider locator2.Provider
	overrides  map[string][]rune
	merged     map[string][]rune
	path       string
	pkg        locator2.Package
}

func (p *provider) ExportedPackages() ([]locator2.Package, error) {
	if err := p.load(); err != nil {
		return nil, err
	}
	return []locator2.Package{p.pkg}, nil
}

func (p *provider) LoadPackage(name string) (locator2.Package, bool, error) {
	if err := p.load(); err != nil {
		return nil, false, err
	}
	if p.pkg.Info().Name == name {
		return p.pkg, true, nil
	}
	return nil, false, nil
}

func (p *provider) load() error {
	pkg, err := p.fsProvider.ExportedPackages()
	if err != nil {
		return err
	}
	if len(pkg) == 0 {
		return fmt.Errorf("failed to load package from %s", p.path)
	}

	for s := range p.merged {
		delete(p.merged, s)
	}

	maps.Copy(p.merged, pkg[0].Sources())
	maps.Copy(p.merged, p.overrides)
	p.pkg = locator2.NewLoadedPackage(pkg[0].Info(), p.merged, p.path)
	return nil
}

func (p *provider) OverrideFile(uri protocol.DocumentURI, content []rune) {
	path := uriToPath(uri)
	if content == nil {
		delete(p.overrides, path)
	} else {
		p.overrides[path] = content
	}
}
