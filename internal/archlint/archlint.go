// Package archlint verifica as regras de dependência da arquitetura hexagonal:
//
//  1. internal/core/** importa só a stdlib e o próprio core.
//  2. Um adapter (internal/adapters/<inbound|outbound>/<nome>) não importa outro.
//  3. Fora de internal/adapters, só cmd/ e test/ importam adapters.
//
// O depguard no golangci-lint cobre a regra 1; este pacote cobre as três e roda
// no go test, então a fronteira vale mesmo sem o linter instalado.
package archlint

import (
	"bufio"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Violation é um import que quebra uma regra.
type Violation struct {
	File   string // relativo à raiz, com /
	Import string
	Rule   string
}

func (v Violation) String() string { return fmt.Sprintf("%s importa %q: %s", v.File, v.Import, v.Rule) }

// ModulePath lê o caminho do módulo no go.mod em root.
func ModulePath(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("go.mod sem diretiva module")
}

// Check percorre os .go em root (pulando testdata, vendor e diretórios
// ocultos) e devolve as violações, ordenadas.
func Check(root, module string) ([]Violation, error) {
	var out []Violation
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if rule := judge(rel, imp, module); rule != "" {
				out = append(out, Violation{File: rel, Import: imp, Rule: rule})
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Import < out[j].Import
	})
	return out, err
}

const (
	coreDir     = "internal/core/"
	adaptersDir = "internal/adapters/"
)

func judge(file, imp, module string) string {
	local, isLocal := strings.CutPrefix(imp, module+"/")
	if imp == module {
		local, isLocal = "", true
	}

	if strings.HasPrefix(file, coreDir) {
		switch {
		case isLocal && !strings.HasPrefix(local+"/", coreDir):
			return "o core só importa o próprio core"
		case !isLocal && !isStdlib(imp):
			return "o core só importa a stdlib"
		}
		return ""
	}
	if !isLocal || !strings.HasPrefix(local, adaptersDir) {
		return ""
	}
	if strings.HasPrefix(file, adaptersDir) {
		if adapterUnit(file) != adapterUnit(local+"/") {
			return "um adapter não importa outro"
		}
		return ""
	}
	if strings.HasPrefix(file, "cmd/") || strings.HasPrefix(file, "test/") {
		return ""
	}
	return "só cmd/ e test/ conhecem adapters"
}

// adapterUnit devolve internal/adapters/<direção>/<nome> de um caminho.
func adapterUnit(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, adaptersDir), "/", 3)
	if len(parts) < 2 {
		return p
	}
	return adaptersDir + parts[0] + "/" + parts[1]
}

// Na stdlib o primeiro elemento do caminho não tem ponto.
func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}
