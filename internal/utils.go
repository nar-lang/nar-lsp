package internal

import (
	"github.com/nar-lang/nar-common/ast"
	"github.com/nar-lang/nar-lsp/internal/protocol"
	"strings"
)

func uriToPath(path protocol.DocumentURI) string {
	if strings.HasPrefix(string(path), "file://") {
		path = path[7:]
	}
	return string(path)
}

func pathToUri(path string) protocol.DocumentURI {
	return protocol.DocumentURI("file://" + path)
}

func locToRange(loc ast.Location) protocol.Range {
	line, c, eline, ec := loc.GetLineAndColumn()
	return protocol.Range{
		Start: protocol.Position{Line: uint32(line - 1), Character: uint32(c - 1)},
		End:   protocol.Position{Line: uint32(eline - 1), Character: uint32(ec - 1)},
	}
}

func locToLocation(loc ast.Location) *protocol.Location {
	return &protocol.Location{
		URI:   pathToUri(loc.FilePath()),
		Range: locToRange(loc),
	}
}
