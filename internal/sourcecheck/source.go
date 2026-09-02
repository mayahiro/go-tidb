// Package sourcecheck analyzes conservative go-tidb usage patterns in Go
// source without loading application packages or connecting to a database.
package sourcecheck

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/check"
)

const (
	ormImportPath   = "github.com/mayahiro/go-tidb/orm"
	modelImportPath = "github.com/mayahiro/go-tidb/model"
)

// ErrNoSourceFiles reports that an input contains no production Go source
// files for the current build context.
var ErrNoSourceFiles = errors.New("source input contains no production Go files for the current build context")

// ErrInvalidSource reports Go source that cannot be parsed or matched to the
// current build context.
var ErrInvalidSource = errors.New("source input is invalid")

// Statistics describes source-analysis coverage without claiming that every
// dynamic query or result flow was understood.
type Statistics struct {
	Files               int `json:"files"`
	ModelTypes          int `json:"model_types"`
	ResultQueries       int `json:"result_queries"`
	QueryPatterns       int `json:"query_patterns"`
	ExplicitProjections int `json:"explicit_projections"`
	Analyzed            int `json:"analyzed"`
	Uncertain           int `json:"uncertain"`
	AnalyzedPatterns    int `json:"analyzed_patterns"`
	UncertainPatterns   int `json:"uncertain_patterns"`
}

// Analysis contains deterministic source statistics and diagnostics.
type Analysis struct {
	Statistics  Statistics         `json:"statistics"`
	Diagnostics []check.Diagnostic `json:"diagnostics"`
}

type sourceInput struct {
	absolutePath string
	displayPath  string
	source       []byte
}

type moduleInfo struct {
	root string
	path string
}

type sourceFile struct {
	file       *ast.File
	packageKey string
	imports    map[string]string
	ormAlias   string
	modelAlias string
}

// AnalyzePath recursively analyzes one directory or analyzes one Go source
// file. Directory analysis excludes tests, generated files, vendor, testdata,
// hidden directories, and files outside the current build context.
func AnalyzePath(path string) (Analysis, error) {
	inputs, err := collectSourceInputs(path)
	if err != nil {
		return Analysis{}, err
	}
	return analyzeInputs(inputs)
}

func collectSourceInputs(path string) ([]sourceInput, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		input, included, err := readSourceInput(absolute, filepath.Base(absolute))
		if err != nil {
			return nil, err
		}
		if !included {
			return nil, ErrNoSourceFiles
		}
		return []sourceInput{input}, nil
	}

	inputs := make([]sourceInput, 0, 16)
	err = filepath.WalkDir(absolute, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if current != absolute && excludedSourceDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(absolute, current)
		if err != nil {
			return err
		}
		input, included, err := readSourceInput(current, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		if included {
			inputs = append(inputs, input)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, ErrNoSourceFiles
	}
	return inputs, nil
}

func readSourceInput(path, display string) (sourceInput, bool, error) {
	name := filepath.Base(path)
	if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
		return sourceInput{}, false, nil
	}
	matched, err := build.Default.MatchFile(filepath.Dir(path), name)
	if err != nil {
		return sourceInput{}, false, fmt.Errorf("%w: invalid build constraints in %s", ErrInvalidSource, display)
	}
	if !matched {
		return sourceInput{}, false, nil
	}
	return sourceInput{absolutePath: path, displayPath: display}, true, nil
}

func excludedSourceDirectory(name string) bool {
	return name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")
}

func analyzeInputs(inputs []sourceInput) (Analysis, error) {
	fileSet := token.NewFileSet()
	files := make([]*sourceFile, 0, len(inputs))
	moduleCache := make(map[string]moduleInfo)
	for _, input := range inputs {
		source := input.source
		if source == nil {
			var err error
			source, err = os.ReadFile(input.absolutePath)
			if err != nil {
				return Analysis{}, err
			}
		}
		parsed, err := parser.ParseFile(fileSet, input.displayPath, source, parser.ParseComments)
		if err != nil {
			return Analysis{}, fmt.Errorf("%w: parse Go source: %v", ErrInvalidSource, err)
		}
		if ast.IsGenerated(parsed) {
			continue
		}
		module := findModule(filepath.Dir(input.absolutePath), moduleCache)
		file := &sourceFile{
			file:       parsed,
			packageKey: sourcePackageKey(filepath.Dir(input.absolutePath), module),
			imports:    sourceImports(parsed),
		}
		file.ormAlias = file.importAlias(ormImportPath)
		file.modelAlias = file.importAlias(modelImportPath)
		files = append(files, file)
	}
	if len(files) == 0 {
		return Analysis{}, ErrNoSourceFiles
	}

	analyzer := newSourceAnalyzer(fileSet, files)
	return analyzer.analyze(), nil
}

func sourceImports(file *ast.File) map[string]string {
	imports := make(map[string]string, len(file.Imports))
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if specification.Name != nil {
			name = specification.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		imports[name] = path
	}
	return imports
}

func (file *sourceFile) importAlias(path string) string {
	for alias, imported := range file.imports {
		if imported == path {
			return alias
		}
	}
	return ""
}

func findModule(directory string, cache map[string]moduleInfo) moduleInfo {
	visited := make([]string, 0, 8)
	current := directory
	for {
		if cached, exists := cache[current]; exists {
			for _, item := range visited {
				cache[item] = cached
			}
			return cached
		}
		visited = append(visited, current)
		goMod := filepath.Join(current, "go.mod")
		if path, ok := readModulePath(goMod); ok {
			result := moduleInfo{root: current, path: path}
			for _, item := range visited {
				cache[item] = result
			}
			return result
		}
		parent := filepath.Dir(current)
		if parent == current {
			for _, item := range visited {
				cache[item] = moduleInfo{}
			}
			return moduleInfo{}
		}
		current = parent
	}
}

func readModulePath(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			path := fields[1]
			if unquoted, err := strconv.Unquote(path); err == nil {
				path = unquoted
			}
			return path, path != ""
		}
	}
	return "", false
}

func sourcePackageKey(directory string, module moduleInfo) string {
	if module.path == "" {
		return "directory:" + filepath.Clean(directory)
	}
	relative, err := filepath.Rel(module.root, directory)
	if err != nil || relative == "." {
		return module.path
	}
	return strings.TrimSuffix(module.path, "/") + "/" + filepath.ToSlash(relative)
}

// FormatStatistics renders one stable human-readable source coverage line.
func FormatStatistics(statistics Statistics) string {
	return fmt.Sprintf(
		"source: files=%d model_types=%d result_queries=%d query_patterns=%d explicit_projections=%d analyzed=%d uncertain=%d analyzed_patterns=%d uncertain_patterns=%d",
		statistics.Files,
		statistics.ModelTypes,
		statistics.ResultQueries,
		statistics.QueryPatterns,
		statistics.ExplicitProjections,
		statistics.Analyzed,
		statistics.Uncertain,
		statistics.AnalyzedPatterns,
		statistics.UncertainPatterns,
	)
}
