package domain

// HarnessDefault is a daemon-owned preference for one harness.
// DisabledModels lists the models clients leave out of their pickers; empty
// means every catalog model, and models a catalog adds later are offered.
// DefaultModel and DefaultEffort are what new sessions run when nothing more
// specific names a model; a launch drops them when the catalogue cannot run
// them. Rows saved by earlier builds may carry model, effort, and
// enabledModels keys; they are ignored.
type HarnessDefault struct {
	DisabledModels []string `json:"disabledModels,omitempty" maxItems:"20000"`
	DefaultModel   string   `json:"defaultModel,omitempty" maxLength:"256"`
	DefaultEffort  string   `json:"defaultEffort,omitempty" maxLength:"64"`
}

// IsZero reports whether the preference holds nothing worth storing.
func (d HarnessDefault) IsZero() bool {
	return len(d.DisabledModels) == 0 && d.DefaultModel == "" && d.DefaultEffort == ""
}
