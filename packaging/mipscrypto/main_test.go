package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssemblyIsCurrent(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("_overlay", "chacha_mipsle.s"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != assemblySource() {
		t.Fatal("regenerate assembly with go run ./packaging/mipscrypto -generate from repository root")
	}
	for _, forbidden := range []string{"CALL", "R22", "R23", "R26", "R27", "R30", "F0"} {
		for _, line := range strings.Split(string(got), "\n") {
			if !strings.HasPrefix(line, "//") && strings.Contains(line, forbidden) {
				t.Fatalf("reserved register, floating point or call in assembly: %s", line)
			}
		}
	}
}

func TestPolyAssemblyIsCurrent(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("_poly1305", "products_mipsle.s"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := os.ReadFile(filepath.Join("_poly1305", "blocks_mipsle.s"))
	if err != nil || string(blocks) != polyBlocksAssemblySource() {
		t.Fatal("regenerate block Poly1305 assembly", err)
	}
	for _, line := range strings.Split(string(blocks), "\n") {
		if strings.HasPrefix(line, "//") {
			continue
		}
		for _, forbidden := range []string{"CALL", "R22", "R23", "R26", "R27", "R30", "F0"} {
			if strings.Contains(line, forbidden) {
				t.Fatalf("reserved register, floating point or call: %s", line)
			}
		}
		if (strings.Contains(line, "BEQ") || strings.Contains(line, "BNE")) && !strings.Contains(line, "R2,") {
			t.Fatalf("branch not controlled by public block count: %s", line)
		}
	}
	if string(got) != productsAssemblySource() {
		t.Fatal("regenerate fused Poly1305 assembly")
	}
	for _, name := range []string{"products_mipsle.s", "mul64_mipsle.s"} {
		data, err := os.ReadFile(filepath.Join("_poly1305", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "//") {
				continue
			}
			for _, forbidden := range []string{"CALL", "R22", "R23", "R26", "R27", "R30", "F0", "BEQ", "BNE"} {
				if strings.Contains(line, forbidden) {
					t.Fatalf("reserved register, floating point, call or branch: %s", line)
				}
			}
		}
	}
}

const polyProductsFixture = `package poly1305
func updateGeneric(state *macState, msg []byte) {
	for len(msg) > 0 {
		h0r0 := mul64(h0, r0)
		h1r0 := mul64(h1, r0)
		h2r0 := mul64(h2, r0)
		h0r1 := mul64(h0, r1)
		h1r1 := mul64(h1, r1)
		h2r1 := mul64(h2, r1)
		consume(h0r0, h1r0, h2r0, h0r1, h1r1, h2r1)
	}
}
`

func TestFusePolyProducts(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		valid        bool
	}{
		{"original", polyProductsFixture, true},
		{"wrong_operand", strings.Replace(polyProductsFixture, "mul64(h2, r1)", "mul64(h2, r0)", 1), false},
		{"missing_product", strings.Replace(polyProductsFixture, "h2r1 := mul64(h2, r1)", "h2r1 := other(h2, r1)", 1), false},
		{"duplicate_groups", strings.Replace(polyProductsFixture, "consume(", "h0r0 := mul64(h0, r0)\nh1r0 := mul64(h1, r0)\nh2r0 := mul64(h2, r0)\nh0r1 := mul64(h0, r1)\nh1r1 := mul64(h1, r1)\nh2r1 := mul64(h2, r1)\nconsume(", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "source.go", tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			fn := file.Decls[0].(*ast.FuncDecl)
			if err := fusePolyProducts(fn); (err == nil) != tc.valid {
				t.Fatalf("fuse error = %v, valid = %v", err, tc.valid)
			}
			if tc.valid {
				var formatted strings.Builder
				if err := format.Node(&formatted, fset, file); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(formatted.String(), "h0r0, h1r0, h2r0, h0r1, h1r1, h2r1 := productsMIPS(h0, h1, h2, r0, r1)") || strings.Contains(formatted.String(), "mul64(") {
					t.Fatalf("incorrect fused variant: %s", &formatted)
				}
			}
		})
	}
}

func TestStagePolyKeepsGeneric(t *testing.T) {
	dst, overlay := t.TempDir(), t.TempDir()
	source := polyProductsFixture + "\nfunc dispatch() { updateGeneric(nil, nil) }\n"
	if err := os.WriteFile(filepath.Join(dst, "sum_generic.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if err := stagePoly1305(dst, overlay); err != nil {
		t.Fatal(err)
	}
	generic, err := os.ReadFile(filepath.Join(dst, "sum_generic.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.Decls[0].(*ast.FuncDecl).Name.Name = "updateGenericGo"
	var want strings.Builder
	if err := format.Node(&want, fset, file); err != nil {
		t.Fatal(err)
	}
	if string(generic) != want.String() {
		t.Fatal("generic algorithm was changed beyond renaming its update function")
	}
	variant, err := os.ReadFile(filepath.Join(dst, "update_mipsle.go"))
	if err != nil || !strings.Contains(string(variant), "productsMIPS(h0, h1, h2, r0, r1)") || !strings.Contains(string(variant), "linux && mipsle && gc && !purego") {
		t.Fatalf("missing CPU-specific variant: %s, %v", variant, err)
	}
}

func TestCopyTree(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(filepath.Join(src, "source.go"), []byte("original"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "source.go"), []byte("patched"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(src, "source.go"))
	if err != nil || string(got) != "original" {
		t.Fatalf("upstream source changed: %q, %v", got, err)
	}
}

func TestMakeArchitectureDispatch(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make unavailable")
	}
	for _, tc := range []struct {
		arch, enabled string
		want          bool
	}{
		{"mipsle", "1", true},
		{"mipsle", "0", false},
		{"arm64", "1", false},
	} {
		cmd := exec.Command("make", "-C", "../..", "-n", "GOARCH="+tc.arch, "MIPS_CRYPTO="+tc.enabled)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make: %v: %s", err, out)
		}
		for _, part := range []string{"go run ./packaging/mipscrypto", "-modfile="} {
			if strings.Contains(string(out), part) != tc.want {
				t.Fatalf("wrong crypto dispatch for %s/%s: %s", tc.arch, tc.enabled, out)
			}
		}
		if !strings.Contains(string(out), "packaging/build_ipk.sh") {
			t.Fatal("default make target no longer builds IPK")
		}
	}
}
