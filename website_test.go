package vortex

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	attrRe   = regexp.MustCompile(`(?i)\b(href|src)="([^"]*)"`)
	testidRe = regexp.MustCompile(`data-testid="([^"]*)"`)
	titleRe  = regexp.MustCompile(`(?s)<title>\s*\S.*?</title>`)
	linkRe   = regexp.MustCompile(`(?i)<link\b[^>]*>`)
)

var sitePages = []string{"index.html", "usage.html", "rust.html"}

// readSite returns the content of a file of website/.
func readSite(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("website", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWebsitePages checks the static rules of every page: the navigation, a title, no
// script, no external resource, no insecure link, and a data-testid used once.
func TestWebsitePages(t *testing.T) {
	for _, page := range sitePages {
		t.Run(page, func(t *testing.T) {
			src := readSite(t, page)
			if !titleRe.MatchString(src) {
				t.Error("no <title>")
			}
			if strings.Contains(strings.ToLower(src), "<script") {
				t.Error("a <script>")
			}
			if strings.Contains(src, "http://") {
				t.Error("a http:// link")
			}
			for _, l := range linkRe.FindAllString(src, -1) {
				if strings.Contains(l, "stylesheet") && !strings.Contains(l, `href="style.css"`) {
					t.Errorf("an external stylesheet: %s", l)
				}
			}
			for _, m := range attrRe.FindAllStringSubmatch(src, -1) {
				attr, val := m[1], m[2]
				if strings.EqualFold(attr, "src") {
					t.Errorf("a src attribute: %s", val)
				}
				checkLink(t, page, val)
			}
			for _, nav := range []string{"nav-home", "nav-usage", "nav-rust"} {
				if !strings.Contains(src, `data-testid="`+nav+`"`) {
					t.Errorf("no %s link in the navigation", nav)
				}
			}
			for _, target := range sitePages {
				if !strings.Contains(src, `<a href="`+target+`"`) {
					t.Errorf("the navigation does not link to %s", target)
				}
			}
			seen := map[string]bool{}
			for _, m := range testidRe.FindAllStringSubmatch(src, -1) {
				if seen[m[1]] {
					t.Errorf("data-testid %q is used twice", m[1])
				}
				seen[m[1]] = true
			}
		})
	}
}

// checkLink checks one href: https links are left alone, a relative one points to a file
// of website/, and to an id of that file when it has a fragment.
func checkLink(t *testing.T, page, href string) {
	t.Helper()
	if strings.HasPrefix(href, "https://") {
		return
	}
	if strings.HasPrefix(href, "/") || strings.Contains(href, "://") {
		t.Errorf("%s: %q is not relative", page, href)
		return
	}
	u, err := url.Parse(href)
	if err != nil {
		t.Errorf("%s: %q: %v", page, href, err)
		return
	}
	file := u.Path
	if file == "" {
		file = page
	}
	if _, err := os.Stat(filepath.Join("website", path.Clean(file))); err != nil {
		t.Errorf("%s: %q: %v", page, href, err)
		return
	}
	if u.Fragment != "" && !strings.Contains(readSite(t, file), `id="`+u.Fragment+`"`) {
		t.Errorf("%s: %q: no id %q in %s", page, href, u.Fragment, file)
	}
}

// TestWebsiteBlocks: the generated blocks are in the pages (the test of internal/gen says
// they are fresh), and the committed rust page holds no number.
func TestWebsiteBlocks(t *testing.T) {
	if s := readSite(t, "index.html"); !strings.Contains(s, `data-testid="code-ExampleRows"`) {
		t.Error("index.html has no example")
	}
	usage := readSite(t, "usage.html")
	for _, name := range []string{"ExampleRows", "ExampleFile_Scan", "ExampleRows_filter", "ExampleFile_Scan_filter", "ExampleNewRowWriter", "ExampleNewRowWriter_readBack", "ExampleNewWriter"} {
		if !strings.Contains(usage, `data-testid="code-`+name+`"`) {
			t.Errorf("usage.html has no %s", name)
		}
	}
	if rust := readSite(t, "rust.html"); !strings.Contains(rust, `data-testid="matrix-table"`) {
		t.Error("rust.html has no support matrix")
	} else if strings.Contains(rust, "<svg") {
		t.Error("rust.html holds a chart: the numbers are filled when the site is built")
	}
}

// TestWebsiteReadme: the README links to the site, outside the generated blocks.
func TestWebsiteReadme(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	if !strings.Contains(readme, "https://fclairamb.github.io/vortex-go/") {
		t.Fatal("README.md does not link to the site")
	}
	for _, name := range []string{"example", "compat", "matrix"} {
		begin, end := "<!-- "+name+":begin -->", "<!-- "+name+":end -->"
		i, j := strings.Index(readme, begin), strings.Index(readme, end)
		if i < 0 || j < i {
			t.Fatalf("no %s block", name)
		}
		if strings.Contains(readme[i:j], "fclairamb.github.io") {
			t.Errorf("the link is inside the %s block", name)
		}
	}
}
