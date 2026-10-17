package litmus

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestLitmusTestServicesFreezeOwnerHome keeps a new test from accidentally
// constructing a service that scans the developer's real home directory.
func TestLitmusTestServicesFreezeOwnerHome(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate owner-home guard")
	}
	backendRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	var unsafe []string
	err := fs.WalkDir(os.DirFS(backendRoot), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		fullPath := filepath.Join(backendRoot, filepath.FromSlash(path))
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, fullPath, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", fullPath, err)
		}
		aliases := litmusImportAliases(parsed)
		parents := ownerHomeParents(parsed)
		ast.Inspect(parsed, func(node ast.Node) bool {
			if literal, ok := node.(*ast.CompositeLit); ok && isLitmusServiceLiteral(literal, parsed.Name.Name, aliases) {
				position := fset.Position(literal.Pos())
				unsafe = append(unsafe, fmt.Sprintf("%s:%d", position.Filename, position.Line))
				return true
			}
			call, ok := node.(*ast.CallExpr)
			if !ok || !isLitmusNew(call, parsed.Name.Name, aliases) {
				return true
			}
			if found, safe := ownerHomeCall(call, parents); found && safe {
				return true
			}
			position := fset.Position(call.Pos())
			unsafe = append(unsafe, fmt.Sprintf("%s:%d", position.Filename, position.Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(unsafe) != 0 {
		t.Fatalf("test services must use WithOwnerHome or newTestService:\n%s", strings.Join(unsafe, "\n"))
	}
}

func TestOwnerHomeGuardRejectsGlobalEnvironment(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package litmus_test
import litmus "github.com/aoagents/agent-orchestrator/backend/internal/litmus"
import "os"
var _ = litmus.New(nil, nil).WithOwnerHome(os.Getenv("HOME"), os.Environ())`, 0)
	if err != nil {
		t.Fatal(err)
	}
	parents := ownerHomeParents(parsed)
	var found, safe bool
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && isLitmusNew(call, parsed.Name.Name, litmusImportAliases(parsed)) {
			found, safe = ownerHomeCall(call, parents)
		}
		return true
	})
	if !found || safe {
		t.Fatalf("global environment passed owner-home guard: found=%v safe=%v", found, safe)
	}
}

func TestWithOwnerHomeCopiesEnvironment(t *testing.T) {
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + filepath.Join(home, "bin")}
	svc := New(NewStore(t.TempDir()), nil).WithOwnerHome(home, env)
	env[0] = "HOME=" + t.TempDir()
	if svc.ownerHome != home || svc.ownerHomeErr != nil || len(svc.ownerEnv) != 2 || svc.ownerEnv[0] != "HOME="+home {
		t.Fatalf("owner fixture was not copied: home=%q err=%v env=%q", svc.ownerHome, svc.ownerHomeErr, svc.ownerEnv)
	}
}

func TestOwnerHomeSnapshotIgnoresLaterEnvironment(t *testing.T) {
	firstHome := t.TempDir()
	env := []string{"HOME=" + firstHome, "PATH=" + filepath.Join(firstHome, "bin")}
	svc := New(NewStore(t.TempDir()), nil).WithOwnerHome(firstHome, env)
	secondHome := t.TempDir()
	t.Setenv("HOME", secondHome)
	t.Setenv("PATH", filepath.Join(secondHome, "bin"))
	if svc.ownerHome != firstHome || svc.ownerHomeErr != nil {
		t.Fatalf("owner home was not captured: %q err=%v", svc.ownerHome, svc.ownerHomeErr)
	}
	if got := environmentValues(svc.ownerEnv)["HOME"]; got != firstHome {
		t.Fatalf("owner environment HOME = %q, want %q", got, firstHome)
	}
	snapshot, err := svc.snapshotHome()
	if err != nil {
		t.Fatal(err)
	}
	homeRoot, otherRoot := filepath.Clean(firstHome), filepath.Clean(secondHome)
	foundHome := false
	for _, root := range snapshot.Roots {
		if filepath.Clean(root.Path) == homeRoot {
			foundHome = true
		}
		if filepath.Clean(root.Path) == otherRoot {
			t.Fatalf("snapshot used the later global HOME %q", secondHome)
		}
	}
	if !foundHome {
		t.Fatalf("snapshot roots omit captured HOME %q: %+v", firstHome, snapshot.Roots)
	}
}

func litmusImportAliases(file *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if !strings.HasSuffix(path, "/internal/litmus") {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" && name != "." {
			aliases[name] = true
		}
	}
	return aliases
}

func isLitmusNew(call *ast.CallExpr, packageName string, aliases map[string]bool) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return packageName == "litmus" && fun.Name == "New"
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		return ok && aliases[pkg.Name] && fun.Sel.Name == "New"
	default:
		return false
	}
}

func ownerHomeParents(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func isLitmusServiceLiteral(literal *ast.CompositeLit, packageName string, aliases map[string]bool) bool {
	switch typ := literal.Type.(type) {
	case *ast.Ident:
		return packageName == "litmus" && typ.Name == "Service"
	case *ast.SelectorExpr:
		pkg, ok := typ.X.(*ast.Ident)
		return ok && aliases[pkg.Name] && typ.Sel.Name == "Service"
	default:
		return false
	}
}

func ownerHomeCall(call *ast.CallExpr, parents map[ast.Node]ast.Node) (found, safe bool) {
	for parent := parents[call]; parent != nil; parent = parents[parent] {
		if selector, ok := parent.(*ast.SelectorExpr); ok && selector.Sel.Name == "WithOwnerHome" {
			withOwner, ok := parents[selector].(*ast.CallExpr)
			if !ok {
				return true, false
			}
			unsafe := false
			for _, arg := range withOwner.Args {
				ast.Inspect(arg, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, pkgOK := selector.X.(*ast.Ident)
					if pkgOK && pkg.Name == "os" && (selector.Sel.Name == "Environ" || selector.Sel.Name == "UserHomeDir" || selector.Sel.Name == "Getenv") {
						unsafe = true
					}
					return true
				})
			}
			return true, !unsafe
		}
	}
	return false, false
}
