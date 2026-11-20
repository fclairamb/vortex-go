// Package gen writes the files that are generated from the code: the example and the support
// matrix of README.md, and api.txt. It uses the standard library only. `go generate ./...`
// rewrites them, and the test of this package fails when one of them is stale.
package gen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fclairamb/vortex-go/internal/edition"
)

// ExampleName is the example whose body is the code block of the README.
const ExampleName = "ExampleRows"

// LineBudget is the number of lines the README example may have.
const LineBudget = 10

// DocFunc returns the output of `go doc -all` for the package in dir.
type DocFunc func(dir string) ([]byte, error)

// GoDoc runs `go doc -all` in dir.
func GoDoc(dir string) ([]byte, error) {
	cmd := exec.Command("go", "doc", "-all", ".")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go doc -all: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ExampleBody returns the body of the function name of the Go source src, without its output
// comment, one tab less indented.
func ExampleBody(src []byte, name string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "example_test.go", src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Body.Lbrace).Offset + 1
		end := fset.Position(fn.Body.Rbrace).Offset
		return dedent(dropOutput(string(src[start:end]))), nil
	}
	return "", fmt.Errorf("no function %s", name)
}

// dropOutput removes the `// Output:` comment, and what follows it, from a function body.
func dropOutput(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "// Output:") || strings.HasPrefix(t, "// Unordered output:") {
			lines = lines[:i]
			break
		}
	}
	return strings.Join(lines, "\n")
}

// dedent removes one tab from each line and the blank lines around the text.
func dedent(body string) string {
	lines := strings.Split(strings.Trim(body, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "\t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// CountLines counts the lines of a code example: the lines that are not blank, not a comment
// and not a lone closing brace.
func CountLines(code string) int {
	n := 0
	for l := range strings.SplitSeq(code, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || t == "}" || strings.HasPrefix(t, "//") {
			continue
		}
		n++
	}
	return n
}

// ExampleBlock is the Markdown of the README example: a Go code block.
func ExampleBlock(body string) string {
	return "```go\n" + strings.ReplaceAll(body, "\t", "    ") + "\n```"
}

// MatrixBlock is the support matrix of the edition: a row per identifier, with what is read
// and what is written.
func MatrixBlock(support []edition.Support) string {
	var b strings.Builder
	b.WriteString("| Kind | Identifier | Read | Written |\n|---|---|---|---|\n")
	for _, s := range support {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", s.Kind, s.ID, yesNo(s.Decoded), yesNo(s.Written))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// Marker returns the begin and end markers of the block name in the README.
func Marker(name string) (begin, end string) {
	return "<!-- " + name + ":begin -->", "<!-- " + name + ":end -->"
}

// WithBlock returns text with what is between the markers of the block name replaced.
func WithBlock(text, name, content string) (string, error) {
	begin, end := Marker(name)
	before, rest, ok := strings.Cut(text, begin)
	if !ok {
		return "", fmt.Errorf("no %s marker", begin)
	}
	_, after, ok := strings.Cut(rest, end)
	if !ok {
		return "", fmt.Errorf("no %s marker", end)
	}
	return before + begin + "\n" + content + "\n" + end + after, nil
}

// Readme returns the README with its example and its matrix up to date.
func Readme(readme string, example []byte, support []edition.Support) (string, error) {
	body, err := ExampleBody(example, ExampleName)
	if err != nil {
		return "", err
	}
	out, err := WithBlock(readme, "example", ExampleBlock(body))
	if err != nil {
		return "", err
	}
	return WithBlock(out, "matrix", MatrixBlock(support))
}

// Run makes README.md and api.txt of the module in root. With write it rewrites the stale
// ones, without it reports them. It returns the names of the files that were stale.
func Run(root string, write bool, doc DocFunc, support []edition.Support) ([]string, error) {
	example, err := os.ReadFile(filepath.Join(root, "example_test.go"))
	if err != nil {
		return nil, err
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return nil, err
	}
	wantReadme, err := Readme(string(readme), example, support)
	if err != nil {
		return nil, fmt.Errorf("README.md: %w", err)
	}
	wantAPI, err := doc(root)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, f := range []struct{ name, want string }{{"README.md", wantReadme}, {"api.txt", string(wantAPI)}} {
		path := filepath.Join(root, f.name)
		have, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if string(have) == f.want {
			continue
		}
		stale = append(stale, f.name)
		if write {
			if err := os.WriteFile(path, []byte(f.want), 0o644); err != nil {
				return nil, err
			}
		}
	}
	return stale, nil
}
