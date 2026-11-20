package gen

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	_ "github.com/fclairamb/vortex-go" // registers the decoders and the encoders
	"github.com/fclairamb/vortex-go/internal/edition"
)

var update = flag.Bool("update", false, "rewrite README.md and api.txt")

// TestGenerate is what `go generate` runs with -update: the README example and matrix, and
// api.txt, must be what the code gives. go doc is only stable on the Go release that wrote
// api.txt, so the other legs of the CI matrix skip the check of api.txt.
func TestGenerate(t *testing.T) {
	doc := GoDoc
	if !strings.HasPrefix(runtime.Version(), apiGo) {
		doc = func(dir string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, "api.txt")) }
	}
	stale, err := Run("../..", *update, doc, edition.Supported())
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 && !*update {
		t.Fatalf("%v stale: run make api", stale)
	}
}

// apiGo is the Go release api.txt is generated with.
const apiGo = "go1.27"

// TestReadmeExampleLines is the line budget of the README example: 10 lines or fewer, error
// handling included.
func TestReadmeExampleLines(t *testing.T) {
	src, err := os.ReadFile("../../example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	body, err := ExampleBody(src, ExampleName)
	if err != nil {
		t.Fatal(err)
	}
	if n := CountLines(body); n > LineBudget {
		t.Fatalf("the README example has %d lines, the budget is %d", n, LineBudget)
	}
}

func TestCountLines(t *testing.T) {
	ok := "a := 1\n// comment\n\nif a > 0 {\n\tuse(a)\n}\n"
	if n := CountLines(ok); n != 2+1 {
		t.Fatalf("%d lines", n)
	}
	eleven := strings.Repeat("f()\n", 11)
	if n := CountLines(eleven); n != 11 || n <= LineBudget {
		t.Fatalf("%d lines, want 11 over the budget", n)
	}
}

func TestExampleBody(t *testing.T) {
	src := "package x\n\nfunc ExampleA() {\n\tfmt.Println(1)\n\t// Output: 1\n}\n\nvar v int\n\nfunc ExampleB() {\n\tif x {\n\t\ty()\n\t}\n}\n\nfunc Decl()\n"
	got, err := ExampleBody([]byte(src), "ExampleA")
	if err != nil || got != "fmt.Println(1)" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = ExampleBody([]byte(src), "ExampleB")
	if err != nil || got != "if x {\n\ty()\n}" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ExampleBody([]byte(src), "ExampleC"); err == nil {
		t.Fatal("no error for a missing function")
	}
	if _, err := ExampleBody([]byte("not go"), "ExampleA"); err == nil {
		t.Fatal("no error for a syntax error")
	}
	if got := dropOutput("a()\n// Unordered output: x\nb()"); got != "a()" {
		t.Fatalf("got %q", got)
	}
}

func TestBlocks(t *testing.T) {
	if got := ExampleBlock("a\n\tb"); got != "```go\na\n    b\n```" {
		t.Fatalf("got %q", got)
	}
	m := MatrixBlock([]edition.Support{
		{Member: edition.Member{Kind: "array", ID: "vortex.a"}, Decoded: true},
		{Member: edition.Member{Kind: "layout", ID: "vortex.b"}, Written: true},
	})
	want := "| Kind | Identifier | Read | Written |\n|---|---|---|---|\n| array | `vortex.a` | yes | no |\n| layout | `vortex.b` | no | yes |"
	if m != want {
		t.Fatalf("got %q", m)
	}
	text := "a\n<!-- x:begin -->\nold\n<!-- x:end -->\nb\n"
	got, err := WithBlock(text, "x", "new")
	if err != nil || got != "a\n<!-- x:begin -->\nnew\n<!-- x:end -->\nb\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"nothing", "<!-- x:begin -->"} {
		if _, err := WithBlock(bad, "x", "y"); err == nil {
			t.Fatalf("%q: no error", bad)
		}
	}
}

const exampleSrc = "package x\n\nfunc ExampleRows() {\n\tf()\n\t// Output: 1\n}\n"

const readmeTmpl = "# t\n<!-- example:begin -->\nold\n<!-- example:end -->\n<!-- matrix:begin -->\nold\n<!-- matrix:end -->\n"

var support = []edition.Support{{Member: edition.Member{Kind: "array", ID: "vortex.a"}, Decoded: true}}

func fakeDoc(out string) DocFunc {
	return func(string) ([]byte, error) { return []byte(out), nil }
}

// tree writes the files Run reads into a temp dir.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadme(t *testing.T) {
	got, err := Readme(readmeTmpl, []byte(exampleSrc), support)
	if err != nil || !strings.Contains(got, "```go\nf()\n```") || !strings.Contains(got, "`vortex.a` | yes | no") {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, run := range map[string]func() error{
		"no function": func() error { _, err := Readme(readmeTmpl, []byte("package x"), support); return err },
		"no example":  func() error { _, err := Readme("# t", []byte(exampleSrc), support); return err },
		"no matrix": func() error {
			_, err := Readme("<!-- example:begin -->\n<!-- example:end -->", []byte(exampleSrc), support)
			return err
		},
	} {
		if run() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestRunFreshAndStale is the three outputs on a temp tree: fresh, stale, and rewritten.
func TestRunFreshAndStale(t *testing.T) {
	dir := tree(t, map[string]string{"example_test.go": exampleSrc, "README.md": readmeTmpl, "api.txt": "old API"})
	doc := fakeDoc("new API\n")
	stale, err := Run(dir, false, doc, support)
	if err != nil || !slices.Equal(stale, []string{"README.md", "api.txt"}) {
		t.Fatalf("stale %v, %v", stale, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "api.txt")); string(b) != "old API" {
		t.Fatal("api.txt was written without write")
	}
	stale, err = Run(dir, true, doc, support)
	if err != nil || len(stale) != 2 {
		t.Fatalf("stale %v, %v", stale, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "api.txt")); string(b) != "new API\n" {
		t.Fatalf("api.txt is %q", b)
	}
	stale, err = Run(dir, false, doc, support)
	if err != nil || len(stale) != 0 {
		t.Fatalf("fresh tree is stale: %v, %v", stale, err)
	}
	// A missing api.txt is stale, not an error.
	if err := os.Remove(filepath.Join(dir, "api.txt")); err != nil {
		t.Fatal(err)
	}
	if stale, err = Run(dir, false, doc, support); err != nil || !slices.Equal(stale, []string{"api.txt"}) {
		t.Fatalf("stale %v, %v", stale, err)
	}
}

func TestRunErrors(t *testing.T) {
	good := map[string]string{"example_test.go": exampleSrc, "README.md": readmeTmpl, "api.txt": "x"}
	without := func(name string) map[string]string {
		m := map[string]string{}
		for k, v := range good {
			if k != name {
				m[k] = v
			}
		}
		return m
	}
	for name, c := range map[string]struct {
		files map[string]string
		doc   DocFunc
	}{
		"no example": {without("example_test.go"), fakeDoc("")},
		"no readme":  {without("README.md"), fakeDoc("")},
		"bad readme": {map[string]string{"example_test.go": exampleSrc, "README.md": "x"}, fakeDoc("")},
		"doc fails":  {good, func(string) ([]byte, error) { return nil, errors.New("boom") }},
	} {
		dir := tree(t, c.files)
		if _, err := Run(dir, false, c.doc, support); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestGoDoc(t *testing.T) {
	out, err := GoDoc("../..")
	if err != nil || !strings.HasPrefix(string(out), "package vortex") {
		t.Fatalf("%.40q, %v", out, err)
	}
	if _, err := GoDoc(t.TempDir()); err == nil {
		t.Fatal("no error outside a module")
	}
}

// TestRunFileErrors: an api.txt that cannot be read, and a README.md that cannot be written.
func TestRunFileErrors(t *testing.T) {
	good := map[string]string{"example_test.go": exampleSrc, "README.md": readmeTmpl}
	dir := tree(t, good)
	if err := os.Mkdir(filepath.Join(dir, "api.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, false, fakeDoc("y"), support); err == nil {
		t.Error("an api.txt that is a directory: no error")
	}
	dir = tree(t, good)
	if err := os.Chmod(filepath.Join(dir, "README.md"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o644); err == nil {
		t.Skip("permissions are not enforced here")
	}
	if _, err := Run(dir, true, fakeDoc("y"), support); err == nil {
		t.Error("a README.md that is read-only: no error")
	}
}

const pageTmpl = "<p>x</p>\n<!-- example:ExampleRows:begin -->\nold\n<!-- example:ExampleRows:end -->\n<!-- matrix:begin -->\nold\n<!-- matrix:end -->\n"

func TestHTMLBlocks(t *testing.T) {
	if got := HTMLCode("ExampleA", "if a < b {\n\tc()\n}"); got != "<pre data-testid=\"code-ExampleA\"><code>if a &lt; b {\n    c()\n}</code></pre>" {
		t.Fatalf("got %q", got)
	}
	m := HTMLMatrix([]edition.Support{{Member: edition.Member{Kind: "array", ID: "vortex.a"}, Decoded: true}})
	for _, want := range []string{`data-testid="matrix-table"`, "<td>array</td><td><code>vortex.a</code></td><td>yes</td><td>no</td>"} {
		if !strings.Contains(m, want) {
			t.Errorf("%q is not in %s", want, m)
		}
	}
}

func TestPage(t *testing.T) {
	got, err := Page(pageTmpl, []byte(exampleSrc), support)
	if err != nil || !strings.Contains(got, `<pre data-testid="code-ExampleRows"><code>f()</code></pre>`) || !strings.Contains(got, "vortex.a") {
		t.Fatalf("got %q, %v", got, err)
	}
	if again, err := Page(got, []byte(exampleSrc), support); err != nil || again != got {
		t.Fatalf("a fresh page changed: %v", err)
	}
	if _, err := Page(pageTmpl, []byte("package x"), support); err == nil {
		t.Error("an example that does not exist: no error")
	}
	// A begin marker with no end marker.
	if _, err := Page("<!-- matrix:begin -->", []byte(exampleSrc), support); err == nil {
		t.Error("no end marker: no error")
	}
	if got, err := Page("no block", nil, support); err != nil || got != "no block" {
		t.Errorf("a page with no block: %q, %v", got, err)
	}
}

// TestRunWebsite: a page of website/ is stale, then rewritten, then fresh; what is not an html
// page is left alone.
func TestRunWebsite(t *testing.T) {
	dir := tree(t, map[string]string{"example_test.go": exampleSrc, "README.md": readmeTmpl, "api.txt": "API"})
	if err := os.Mkdir(filepath.Join(dir, "website"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"index.html": pageTmpl, "style.css": "x"} {
		if err := os.WriteFile(filepath.Join(dir, "website", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	doc := fakeDoc("API")
	if _, err := Run(dir, true, doc, support); err != nil {
		t.Fatal(err)
	}
	stale, err := Run(dir, false, doc, support)
	if err != nil || len(stale) != 0 {
		t.Fatalf("fresh tree is stale: %v, %v", stale, err)
	}
	page := filepath.Join(dir, "website", "index.html")
	b, _ := os.ReadFile(page)
	if err := os.WriteFile(page, []byte(strings.Replace(string(b), "f()", "g()", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale, err = Run(dir, false, doc, support); err != nil || !slices.Equal(stale, []string{"website/index.html"}) {
		t.Fatalf("stale %v, %v", stale, err)
	}
}

func TestRunWebsiteErrors(t *testing.T) {
	good := map[string]string{"example_test.go": exampleSrc, "README.md": readmeTmpl}
	// website is a file: it cannot be listed.
	dir := tree(t, map[string]string{"example_test.go": exampleSrc, "README.md": readmeTmpl, "website": "x"})
	if _, err := Run(dir, false, fakeDoc(""), support); err == nil {
		t.Error("website is a file: no error")
	}
	// A page that is a directory cannot be read.
	dir = tree(t, good)
	if err := os.MkdirAll(filepath.Join(dir, "website", "a.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, false, fakeDoc(""), support); err == nil {
		t.Error("a page that is a directory: no error")
	}
	// A page with an unknown example.
	dir = tree(t, good)
	if err := os.Mkdir(filepath.Join(dir, "website"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "<!-- example:Nope:begin -->\n<!-- example:Nope:end -->"
	if err := os.WriteFile(filepath.Join(dir, "website", "a.html"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, false, fakeDoc(""), support); err == nil || !strings.Contains(err.Error(), "website/a.html") {
		t.Errorf("an unknown example: %v", err)
	}
}
