package backend

import "testing"

// Real `go version -m ~/go/bin/gopls` output: the leading version line, then the
// build-info block. We care about the path and mod lines.
const goVersionMSample = `/home/user/go/bin/gopls: go1.25.5
	path	golang.org/x/tools/gopls
	mod	golang.org/x/tools/gopls	v0.14.2	h1:abc=
	dep	golang.org/x/mod	v0.33.0	h1:def=
	build	-buildmode=exe
`

func TestParseGoVersionM(t *testing.T) {
	b, ok := parseGoVersionM("gopls", goVersionMSample)
	if !ok {
		t.Fatal("parseGoVersionM: ok = false, want true")
	}
	if b.name != "gopls" || b.pkg != "golang.org/x/tools/gopls" ||
		b.module != "golang.org/x/tools/gopls" || b.version != "v0.14.2" {
		t.Errorf("parseGoVersionM = %+v, want name=gopls pkg=mod=golang.org/x/tools/gopls version=v0.14.2", b)
	}
}

// The package path can differ from the module path (a /cmd/... subpackage).
func TestParseGoVersionMSubpackage(t *testing.T) {
	const out = `/home/user/go/bin/callgraph: go1.25.5
	path	golang.org/x/tools/cmd/callgraph
	mod	golang.org/x/tools	v0.42.0	h1:abc=
`
	b, ok := parseGoVersionM("callgraph", out)
	if !ok {
		t.Fatal("parseGoVersionM: ok = false, want true")
	}
	if b.module != "golang.org/x/tools" || b.pkg != "golang.org/x/tools/cmd/callgraph" {
		t.Errorf("parseGoVersionM = %+v, want module=golang.org/x/tools pkg=.../cmd/callgraph", b)
	}
}

// Binaries built from local source, and anything that isn't a Go binary, have no
// upstream version and must be skipped.
func TestParseGoVersionMSkips(t *testing.T) {
	cases := map[string]string{
		"devel":   "/home/user/go/bin/x: go1.25.5\n\tpath\texample.com/x\n\tmod\texample.com/x\t(devel)\t\n",
		"no mod":  "/home/user/go/bin/x: go1.25.5\n\tpath\texample.com/x\n",
		"not go":  "some random file contents\n",
		"no path": "/home/user/go/bin/x: go1.25.5\n\tmod\texample.com/x\tv1.0.0\th1:z=\n",
	}
	for name, out := range cases {
		if _, ok := parseGoVersionM("x", out); name != "no path" && ok {
			t.Errorf("%s: parseGoVersionM ok = true, want false", name)
		}
	}
	// "no path" is still valid — pkg falls back to the module path.
	if b, ok := parseGoVersionM("x", cases["no path"]); !ok || b.pkg != "example.com/x" {
		t.Errorf("no path: got %+v ok=%v, want pkg falls back to module", b, ok)
	}
}
