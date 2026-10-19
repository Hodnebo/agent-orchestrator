package ports

// ChatDiagnosticPolicy reports optional native controls for support jobs.
// Their scope depends on the provider; inherited integrations may act outside
// the native command sandbox. They do not establish uniform process isolation.
type ChatDiagnosticPolicy struct {
	ReadOnly           bool
	DisableToolNetwork bool
}

// ChatDiagnosticDriver supplies extra native controls where implemented.
// It is not a requirement for support jobs: ordinary Chat permission admission
// still applies to drivers without this optional profile.
type ChatDiagnosticDriver interface {
	DiagnosticPolicy() ChatDiagnosticPolicy
}
