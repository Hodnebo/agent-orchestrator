package triage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/modelcatalog"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	configuredCatalogVersion = 1
	maxConfiguredCatalogSize = 256 << 10
)

type configuredCatalog struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Choices   []configuredChoice `json:"choices"`
}

// The stored projection has no credential, executable, environment or path
// fields. Availability is checked when the chooser is opened.
type configuredChoice struct {
	Harness       string                `json:"harness"`
	Models        []ModelChoice         `json:"models"`
	ModelsMissing bool                  `json:"modelsMissing,omitempty"`
	Accounts      []Account             `json:"accounts,omitempty"`
	Permissions   domain.PermissionMode `json:"permissions,omitempty"`
	Mode          string                `json:"mode,omitempty"`
}

// Catalog reads a small configuration snapshot without opening AO's database
// or parsing its daemon configuration. Its first-run discovery reads only known
// native credential evidence, never installation or sign-in commands.
type Catalog struct {
	dataDir string
	mu      sync.Mutex
	labels  map[string]string
	resolve map[string]func(context.Context) (string, error)
	support func(string) (bool, string)
	native  func(context.Context) []Choice
	now     func() time.Time
}

// NewCatalog builds the daemon-independent configured-agent chooser.
func NewCatalog(dataDir string) *Catalog {
	c := &Catalog{
		dataDir: dataDir, labels: map[string]string{},
		resolve: map[string]func(context.Context) (string, error){},
		support: defaultTriageSupport, now: func() time.Time { return time.Now().UTC() },
	}
	for _, item := range agentregistry.Harnessed() {
		id := string(item.Harness)
		c.labels[id] = item.Manifest.Name
		if resolver, ok := item.Agent.(interface {
			ResolveBinary(context.Context) (string, error)
		}); ok {
			c.resolve[id] = resolver.ResolveBinary
		}
		if resolver, ok := item.Agent.(interface {
			ResolveBinaryPresence(context.Context) (string, error)
		}); ok {
			c.resolve[id] = resolver.ResolveBinaryPresence
		}
	}
	c.native = nativeConfiguredChoices
	return c
}

// SetSupport supplies the host's transport compatibility check. The selected
// provider's normal permission admission runs before its first turn.
func (c *Catalog) SetSupport(support func(string) (bool, string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if support == nil {
		support = defaultTriageSupport
	}
	c.support = support
}

func defaultTriageSupport(string) (bool, string) {
	return false, "Agent compatibility has not been loaded by the support helper."
}

func sameNativeAccount(saved, native []Account) bool {
	if saved == nil {
		return true
	}
	if len(saved) == 0 {
		return len(native) == 0
	}
	selected := ""
	for _, account := range saved {
		if account.Default {
			if selected != "" {
				return false
			}
			selected = account.ID
		}
	}
	if selected == "" && len(saved) == 1 {
		selected = saved[0].ID
	}
	if selected == "" {
		return false
	}
	for _, account := range native {
		if account.ID == selected {
			return true
		}
	}
	return false
}

// Configure replaces the explicitly configured choices. It never supplements
// their models with the provider's full public model catalog.
func (c *Catalog) Configure(ctx context.Context, choices []Choice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.dataDir) == "" {
		return errors.New("triage data directory is required")
	}
	snapshot := configuredCatalog{Version: configuredCatalogVersion, UpdatedAt: c.now(), Choices: c.filter(choices)}
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > maxConfiguredCatalogSize {
		return errors.New("configured triage choices exceed the size limit")
	}
	for _, dir := range []string{c.dataDir, filepath.Join(c.dataDir, "reports"), c.root()} {
		if err := privateDir(dir); err != nil {
			return errors.New("cannot prepare private triage configuration storage")
		}
	}
	if err := atomicPrivateJSON(c.path(), snapshot); err != nil {
		return errors.New("cannot save configured triage choices")
	}
	return nil
}

func (c *Catalog) root() string { return filepath.Join(c.dataDir, "reports", "triage") }
func (c *Catalog) path() string { return filepath.Join(c.root(), "configured.json") }

// Choices returns only configured entries. Saved timestamps are retained even
// when old so a disconnected chooser can show the age of its configuration.
func (c *Catalog) Choices(ctx context.Context) ([]Choice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.dataDir) == "" {
		return nil, errors.New("triage data directory is required")
	}
	snapshot, err := c.load(ctx)
	choices := make([]Choice, 0, len(snapshot.Choices))
	source := "configured-snapshot"
	updatedAt := snapshot.UpdatedAt
	nativeModels := map[string]bool{}
	if errors.Is(err, os.ErrNotExist) {
		source, updatedAt = "native-configuration", c.now()
		choices = c.native(ctx)
	} else if err != nil {
		return nil, err
	} else {
		missing := false
		for _, choice := range snapshot.Choices {
			choices = append(choices, Choice{Harness: choice.Harness, Models: choice.Models, ModelsMissing: choice.ModelsMissing, Accounts: choice.Accounts, Permissions: choice.Permissions, Mode: choice.Mode})
			missing = missing || choice.ModelsMissing
		}
		// Only an explicitly marked absent catalog permits native discovery.
		// Omitted harnesses and intentional empty model/account lists never
		// acquire replacement configuration from another login.
		if missing {
			native := map[string]Choice{}
			for _, choice := range c.native(ctx) {
				native[choice.Harness] = choice
			}
			for index := range choices {
				choice := &choices[index]
				configured, ok := native[choice.Harness]
				if !choice.ModelsMissing || len(choice.Models) != 0 || !ok || len(configured.Models) == 0 || !sameNativeAccount(choice.Accounts, configured.Accounts) {
					continue
				}
				choice.Models = configured.Models
				choice.ModelsMissing = false
				if choice.Accounts == nil {
					choice.Accounts = configured.Accounts
				}
				nativeModels[choice.Harness] = true
			}
		}
	}
	filtered := c.filter(choices)
	out := make([]Choice, 0, len(filtered))
	for _, item := range filtered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		choice := Choice{Harness: item.Harness, Label: c.labels[item.Harness], Models: item.Models, ModelsMissing: item.ModelsMissing,
			Accounts: item.Accounts, Source: source, UpdatedAt: updatedAt, Permissions: item.Permissions, Mode: item.Mode}
		if nativeModels[item.Harness] {
			choice.Source = "native-configuration"
			choice.UpdatedAt = c.now()
		}
		choice.Available, choice.UnavailableReason = c.support(item.Harness)
		if len(choice.Models) == 0 {
			choice.Available = false
			choice.UnavailableReason = "No configured models are available for this agent."
		} else if _, usesAccounts := domain.AgentAccountProviderFor(domain.AgentHarness(choice.Harness)); usesAccounts && len(choice.Accounts) == 0 {
			choice.Available = false
			choice.UnavailableReason = "Configured account information is unavailable; refresh this agent in AO."
		}
		if choice.Available {
			resolve := c.resolve[item.Harness]
			if resolve == nil {
				choice.Available = false
			} else {
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, resolveErr := resolve(probeCtx)
				cancel()
				choice.Available = resolveErr == nil
			}
			if !choice.Available {
				choice.UnavailableReason = "The configured agent executable is unavailable."
			}
		}
		if !choice.Available && choice.UnavailableReason == "" {
			choice.UnavailableReason = "This configured agent is unavailable for triage."
		}
		out = append(out, choice)
	}
	return out, nil
}

func (c *Catalog) load(ctx context.Context) (configuredCatalog, error) {
	var snapshot configuredCatalog
	for _, dir := range []string{c.dataDir, filepath.Join(c.dataDir, "reports"), c.root()} {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, os.ErrNotExist
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return snapshot, errors.New("saved triage configuration directory is unavailable or unsafe")
		}
	}
	info, err := os.Lstat(c.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, os.ErrNotExist
		}
		return snapshot, errors.New("cannot read saved triage configuration")
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfiguredCatalogSize {
		return snapshot, errors.New("saved triage configuration is invalid; refresh configured agents")
	}
	file, err := os.Open(c.path()) //nolint:gosec // Fixed file beneath the supplied AO data directory.
	if err != nil {
		return snapshot, errors.New("cannot read saved triage configuration")
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfiguredCatalogSize+1))
	if decoder.Decode(&snapshot) != nil || snapshot.Version != configuredCatalogVersion || snapshot.UpdatedAt.IsZero() {
		return configuredCatalog{}, errors.New("saved triage configuration is invalid; refresh configured agents")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return configuredCatalog{}, errors.New("saved triage configuration is invalid; refresh configured agents")
	}
	return snapshot, nil
}

func (c *Catalog) filter(choices []Choice) []configuredChoice {
	out := make([]configuredChoice, 0, len(choices))
	seen := map[string]bool{}
	for _, choice := range choices {
		if _, known := c.labels[choice.Harness]; !known || seen[choice.Harness] {
			continue
		}
		config := domain.AgentConfig{Permissions: choice.Permissions, Mode: choice.Mode}
		if config.Validate() != nil {
			continue
		}
		item := configuredChoice{Harness: choice.Harness, Models: []ModelChoice{}, ModelsMissing: choice.ModelsMissing && len(choice.Models) == 0, Permissions: choice.Permissions, Mode: choice.Mode}
		models := map[string]bool{}
		for _, model := range choice.Models {
			if models[model.ID] || !validConfiguredModel(choice.Harness, model.ID) {
				continue
			}
			label := catalogLabel(model.Label, model.ID)
			if model.ID == "" {
				label = "Configured default"
			}
			clean := ModelChoice{ID: model.ID, Label: label, Default: model.Default}
			efforts := map[string]bool{}
			for _, effort := range model.Efforts {
				if validCatalogID(effort, false) && !efforts[effort] {
					clean.Efforts = append(clean.Efforts, effort)
					efforts[effort] = true
				}
			}
			item.Models = append(item.Models, clean)
			models[model.ID] = true
		}
		modelDefaults := 0
		for _, model := range item.Models {
			if model.Default {
				modelDefaults++
			}
		}
		if modelDefaults > 1 {
			for i := range item.Models {
				item.Models[i].Default = false
			}
		}
		accounts := map[string]bool{}
		for _, account := range choice.Accounts {
			if validConfiguredAccount(choice.Harness, account.ID) && !accounts[account.ID] {
				item.Accounts = append(item.Accounts, Account{ID: account.ID, Label: catalogLabel(account.Label, "Saved account"), Default: account.Default})
				accounts[account.ID] = true
			}
		}
		if len(choice.Accounts) > 0 && len(item.Accounts) == 0 {
			continue
		}
		accountDefaults := 0
		for _, account := range item.Accounts {
			if account.Default {
				accountDefaults++
			}
		}
		if accountDefaults > 1 {
			for i := range item.Accounts {
				item.Accounts[i].Default = false
			}
		}
		out = append(out, item)
		seen[choice.Harness] = true
	}
	return out
}

func validConfiguredModel(harness, id string) bool {
	return (id == "" || validCatalogID(id, true)) && modelcatalog.Accepts(harness, id)
}

func validCatalogID(id string, slash bool) bool {
	if id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.HasPrefix(id, "-") || strings.HasPrefix(id, "/") || strings.Contains(id, "..") || Redact(id) != id {
		return false
	}
	for _, r := range id {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.:@+", r) && (!slash || r != '/') {
			return false
		}
	}
	return true
}

func catalogLabel(label, fallback string) string {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 160 || !utf8.ValidString(label) || Redact(label) != label || strings.ContainsAny(label, "/\\~") {
		return fallback
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return fallback
		}
	}
	return label
}

// LaunchEnv validates the exact advertised selection and resolves only its
// explicit account. It never chooses a model, account or provider on failure.
func (c *Catalog) LaunchEnv(ctx context.Context, selection Selection) (map[string]string, error) {
	_, env, err := c.ResolveSelection(ctx, selection)
	return env, err
}

// ResolveSelection inherits only this configured choice's own saved settings.
// A caller cannot change its permission policy or agent-owned mode at launch.
func (c *Catalog) ResolveSelection(ctx context.Context, selection Selection) (Selection, map[string]string, error) {
	choices, err := c.Choices(ctx)
	if err != nil {
		return selection, nil, err
	}
	for _, choice := range choices {
		if choice.Harness != selection.Harness {
			continue
		}
		if !choice.Available {
			return selection, nil, errors.New(choice.UnavailableReason)
		}
		if selection.Permissions != "" && selection.Permissions != choice.Permissions || selection.Mode != "" && selection.Mode != choice.Mode {
			return selection, nil, errors.New("the selected agent settings have changed; choose the agent again")
		}
		selection.Permissions, selection.Mode = choice.Permissions, choice.Mode
		var model *ModelChoice
		for i := range choice.Models {
			if choice.Models[i].ID == selection.Model {
				model = &choice.Models[i]
				break
			}
		}
		if model == nil {
			return selection, nil, errors.New("the selected model is not configured for triage")
		}
		if selection.Effort != "" {
			found := false
			for _, effort := range model.Efforts {
				found = found || effort == selection.Effort
			}
			if !found {
				return selection, nil, errors.New("the selected effort is not configured for this model")
			}
		}
		if selection.AccountID == "" && len(choice.Accounts) == 0 {
			return selection, map[string]string{}, nil
		}
		for _, account := range choice.Accounts {
			if selection.AccountID != "" && account.ID == selection.AccountID {
				env, err := configuredAccountEnv(ctx, c.dataDir, selection.Harness, account.ID)
				return selection, env, err
			}
		}
		return selection, nil, errors.New("select a configured account for triage")
	}
	return selection, nil, errors.New("the selected agent is not configured for triage")
}
