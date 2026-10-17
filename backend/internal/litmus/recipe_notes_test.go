package litmus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAOTesterNotesSurviveRecipeExcerpt(t *testing.T) {
	recipe, err := os.ReadFile("recipes/ao-recipe.yaml")
	is(t, "")(nil, err)
	pack := renderPack(intakeData{repo: "orchestratorinc/agent-orchestrator", pr: pullRequest{Number: 1}}, string(recipe))
	for _, note := range []string{
		"You may create AO Cloud projects.",
		"Clone a repo accepts a typed URL and destination, avoiding the native folder picker.",
		"GET `/api/v1/agents/{agent}/update-advisory?refresh=true` on the leg's daemon.",
	} {
		if !strings.Contains(pack, note) {
			t.Errorf("tester context pack omitted recipe note %q", note)
		}
	}
}

func TestRecipeTextUsesEmbeddedDefaultWhenStoreIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	recipe, err := store.recipeText(DefaultRepo)
	is(t, "")(recipe, err)
	if recipe == "" {
		t.Fatal("recipeText returned no recipe for the default repo")
	}
	pack := renderPack(intakeData{repo: DefaultRepo, pr: pullRequest{Number: 1}}, recipe)
	for _, note := range []string{
		"You may create AO Cloud projects.",
		"Clone a repo accepts a typed URL and destination, avoiding the native folder picker.",
		"GET `/api/v1/agents/{agent}/update-advisory?refresh=true` on the leg's daemon.",
	} {
		if !strings.Contains(pack, note) {
			t.Errorf("embedded recipe note %q missing from empty-store context pack", note)
		}
	}
}

func TestRecipeNotesRenderOutsideTheYAMLWordCap(t *testing.T) {
	store := NewStore(t.TempDir())
	const note = "Keep this tester guidance after the long recipe excerpt."
	raw := "kind: cli\nrun:\n  cmd: \"" + strings.Repeat("filler ", 205) + "\"\nnotes: |\n  " + note + "\n"
	writeFile(t, store.recipePath("o/r"), raw)
	recipe, err := store.recipeText("o/r")
	is(t, "")(recipe, err)
	pack := renderPack(intakeData{repo: "o/r", pr: pullRequest{Number: 1}}, recipe)
	if !strings.Contains(pack, "[truncated at 200 words]") || !strings.Contains(pack, "```\n\n### Tester notes\n\n"+note) {
		t.Fatalf("recipe excerpt cap or separate notes missing:\n%s", pack)
	}
	start := strings.Index(pack, "```yaml\n")
	end := strings.Index(pack[start+8:], "```") + start + 8
	if strings.Contains(pack[start:end], note) || strings.Contains(pack[start:end], "notes:") {
		t.Fatal("tester notes were included in the capped YAML excerpt")
	}
}

func TestRecipeTextUsesStoredOverrideWithoutHidingReadErrors(t *testing.T) {
	store := NewStore(t.TempDir())
	writeFile(t, store.recipePath(DefaultRepo), "kind: cli\nrun: {cmd: fixture}\nnotes: Stored tester guidance.\n")
	recipe, err := store.recipeText(DefaultRepo)
	is(t, "")(recipe, err)
	loaded, err := store.LoadRecipe(DefaultRepo)
	is(t, "")(loaded, err)
	if !strings.Contains(recipe, "Stored tester guidance.") || loaded.Notes != "Stored tester guidance." {
		t.Fatalf("stored override was lost: text=%q loaded=%+v", recipe, loaded)
	}
	if err := os.Remove(store.recipePath(DefaultRepo)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.recipePath(DefaultRepo), 0o700); err != nil {
		t.Fatal(err)
	}
	if text, err := store.recipeText(DefaultRepo); err == nil || text != "" {
		t.Fatalf("unreadable recipe was hidden by default: text=%q err=%v", text, err)
	}
}

func TestRecipeTextReportsInvalidRecipeAndLeavesMissingOptional(t *testing.T) {
	store := NewStore(t.TempDir())
	if recipe, err := store.recipeText("o/r"); recipe != "" || err != nil {
		t.Fatalf("missing optional recipe = %q, %v; want empty and no error", recipe, err)
	}
	if err := os.MkdirAll(filepath.Dir(store.recipePath("o/r")), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.recipePath("o/r"), []byte("kind: invalid\nrun: {cmd: run}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.recipeText("o/r")
	is(t, "TESTING_RECIPE_INVALID")(nil, err)
}
