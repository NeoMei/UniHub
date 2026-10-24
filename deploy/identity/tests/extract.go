// Copyright 2026 UniHub contributors. Licensed under Apache-2.0.
// Extract verbatim upstream declarations, so tests exercise the pinned source
// functions rather than an independently rewritten sanitizer.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	source := flag.String("source", "", "Verified public upstream source tree")
	out := flag.String("out", "", "Disposable harness output")
	flag.Parse()
	if *source == "" || *out == "" {
		panic("source and out required")
	}
	for _, dir := range []string{"object", "conf", "util"} {
		if err := os.MkdirAll(filepath.Join(*out, dir), 0755); err != nil {
			panic(err)
		}
	}
	record := selectDeclarations(filepath.Join(*source, "object/record.go"), map[string]bool{"init": true, "maskSecrets": true, "NewRecord": true, "AddRecord": true}, true)
	imports := `package object
import (
 "encoding/json"
 "fmt"
 "regexp"
 "strings"
 "github.com/beego/beego/v2/server/web/context"
 "github.com/casdoor/casdoor/conf"
 "github.com/casdoor/casdoor/util"
)
`
	write(filepath.Join(*out, "object/record_source.go"), imports+record)
	filter := selectDeclarations(filepath.Join(*source, "util/path.go"), map[string]bool{"FilterQuery": true}, false)
	write(filepath.Join(*out, "util/filter_source.go"), "package util\nimport (\"net/url\";\"strings\")\n"+filter)
}
func selectDeclarations(path string, functions map[string]bool, includeData bool) string {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, 0)
	if err != nil {
		panic(err)
	}
	var parts []string
	found := map[string]bool{}
	for _, decl := range file.Decls {
		include := false
		switch d := decl.(type) {
		case *ast.FuncDecl:
			include = d.Recv == nil && functions[d.Name.Name]
			if include {
				found[d.Name.Name] = true
			}
		case *ast.GenDecl:
			include = includeData && (d.Tok == token.VAR || d.Tok == token.TYPE)
		}
		if include {
			parts = append(parts, string(data[fset.Position(decl.Pos()).Offset:fset.Position(decl.End()).Offset]))
		}
	}
	for name := range functions {
		if !found[name] {
			panic(fmt.Sprintf("missing pinned function %s", name))
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}
func write(path, data string) {
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		panic(err)
	}
}
