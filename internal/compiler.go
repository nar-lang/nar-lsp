package internal

import (
	"context"
	"errors"
	"fmt"
	"github.com/nar-lang/nar-common/ast"
	"github.com/nar-lang/nar-common/common"
	"github.com/nar-lang/nar-common/logger"
	"github.com/nar-lang/nar-compiler/compiler"
	"github.com/nar-lang/nar-lsp/internal/protocol"
	"os"
	"runtime/debug"
	"time"
)

func (s *server) compiler(ctx context.Context) {
	modifiedDocs := map[protocol.DocumentURI]struct{}{}
	modifiedPackages := map[ast.PackageIdentifier]struct{}{}

	doc := <-s.compileChan
	modifiedDocs[doc.uri] = struct{}{}

	for {
		waitTimeout := true
		for waitTimeout {
			select {
			case doc = <-s.compileChan:
				modifiedDocs[doc.uri] = struct{}{}
				continue
			case <-time.After(500 * time.Millisecond):
				waitTimeout = false
				break
			case <-ctx.Done():
				return
			}
		}

		if len(modifiedDocs) == 0 {
			continue
		}

		s.locker.Lock()

		for name, mod := range s.parsedModules {
			for uri := range modifiedDocs {
				path := uriToPath(uri)
				if mod.Location().FilePath() == path {
					delete(s.parsedModules, name)
					delete(s.normalizedModules, name)
					delete(s.typedModules, name)
					modifiedPackages[mod.PackageName()] = struct{}{}
					delete(modifiedDocs, uri)
					break
				}
			}
		}

		for uri := range modifiedDocs {
			delete(modifiedDocs, uri)
			pkgName := s.packageRootToName[s.documentToPackageRoot[uri]]
			if pkgName != "" {
				modifiedPackages[pkgName] = struct{}{}
			}
		}

		s.locker.Unlock()

		s.compile()

		for name := range modifiedPackages {
			delete(modifiedPackages, name)
		}

		select {
		case s.compiledChan <- struct{}{}:
		default:
		}
	}
}

func (s *server) compile() {
	defer func() {
		if r := recover(); r != nil {
			s.reportError(fmt.Sprintf("internal error:\n%v\n\n%s", r, debug.Stack()))

		}
	}()
	log := &logger.LogWriter{}

	_, affectedModuleNames := compiler.CompileEx(
		log, s.locator, nil, true, s.parsedModules, s.normalizedModules, s.typedModules)

	diagnosticData := s.extractDiagnosticsData(log)
	if len(diagnosticData) == 0 {
		s.log.Flush(os.Stdout)
	}

	for _, moduleName := range affectedModuleNames {
		if mod, ok := s.parsedModules[moduleName]; ok {
			uri := pathToUri(mod.Location().FilePath())
			if _, reported := diagnosticData[uri]; !reported {
				s.notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
					URI:         uri,
					Diagnostics: []protocol.Diagnostic{},
				})
			}
		}
	}

	for uri, dsx := range diagnosticData {
		s.notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: dsx,
		})
	}
}

func (s *server) extractDiagnosticsData(log *logger.LogWriter) map[protocol.DocumentURI][]protocol.Diagnostic {
	diagnosticsData := map[protocol.DocumentURI][]protocol.Diagnostic{}

	insertDiagnostic := func(e common.ErrorWithLocation, severity protocol.DiagnosticSeverity) {
		uri := pathToUri(e.Location().FilePath())
		diagnosticsData[uri] = append(diagnosticsData[uri],
			protocol.Diagnostic{
				Range:              locToRange(e.Location()),
				Severity:           protocol.SeverityError,
				Message:            e.Message(),
				RelatedInformation: nil,
			})
	}

	for _, err := range log.Errors() {
		var ewl common.ErrorWithLocation
		if errors.As(err, &ewl) {
			insertDiagnostic(ewl, protocol.SeverityError)
		} else {
			s.log.Err(err)
		}
	}

	for _, err := range log.Warnings() {
		var ewl common.ErrorWithLocation
		if errors.As(err, &ewl) {
			insertDiagnostic(ewl, protocol.SeverityWarning)
		} else {
			s.log.Warn(err)
		}
	}

	for _, msg := range log.Messages() {
		s.log.Info(msg)
	}

	return diagnosticsData
}
