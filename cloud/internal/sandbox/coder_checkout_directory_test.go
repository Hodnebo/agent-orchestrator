package sandbox

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A project's working-folder checkout is stamped on an ao-worker session's
// profile, on a bring-your-own connection and on the deployment Coder alike;
// never with the workspace worker user, and a session without one keeps the
// historical profile shape.
func TestCoderPlanStampsTheWorkingFolderCheckout(t *testing.T) {
	const templateID = "2a2e262c-b31c-4202-946d-a19ad45d1fd2"
	options := &CoderSessionOptions{CheckoutDirectory: "~/work/alice", CheckoutSiblings: []string{"billing"}}
	byo := ProvisioningDefaults{Provider: ProviderCoder, Coder: CoderConfig{WorkerWorkingFolder: true}}
	override := CoderDeploymentOverride{
		BaseURL: "https://coder.acme.example", Owner: "dev", TemplateID: templateID, DurableRoot: "~",
	}
	deployment := ProvisioningDefaults{Provider: ProviderCoder, Coder: CoderConfig{
		BaseURL: "https://coder.ao.example", Owner: "ao", TemplateID: templateID, DurableRoot: "/home/coder",
		WorkerTokenTTL: DefaultWorkerTokenTTL, WorkerWorkingFolder: true,
	}}
	// AO_CLOUD_CODER_WORKER_WORKING_FOLDER off: nothing is stamped, so a
	// release that ignores the field is a safe rollback target.
	flagOff := deployment
	flagOff.Coder.WorkerWorkingFolder = false
	workspaceUser := override
	workspaceUser.WorkerUser = CoderWorkerUserWorkspace
	for _, tc := range []struct {
		name         string
		defaults     ProvisioningDefaults
		options      *CoderSessionOptions
		override     *CoderDeploymentOverride
		wantCheckout string
		wantSiblings []string
	}{
		{"byo ao-worker", byo, options, &override, "~/work/alice", []string{"billing"}},
		{"deployment coder", deployment, options, nil, "~/work/alice", []string{"billing"}},
		{"workspace user", byo, options, &workspaceUser, "", nil},
		{"no working folder", deployment, nil, nil, "", nil},
		{"working folder off", flagOff, options, nil, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := tc.defaults.SessionPlanForProviderWithCoder("claude-code", ProviderCoder, tc.options, tc.override)
			if err != nil {
				t.Fatal(err)
			}
			profile := decodePlanCoderProfile(t, plan)
			if profile.CheckoutDirectory != tc.wantCheckout || !slices.Equal(profile.CheckoutSiblings, tc.wantSiblings) {
				t.Fatalf("profile checkout = %q %q, want %q %q", profile.CheckoutDirectory, profile.CheckoutSiblings,
					tc.wantCheckout, tc.wantSiblings)
			}
			if tc.wantCheckout == "" && strings.Contains(string(plan.ResourceProfile), "checkout") {
				t.Fatalf("profile carries a checkout: %s", plan.ResourceProfile)
			}
		})
	}
	bad := &CoderSessionOptions{CheckoutDirectory: "~/work/{repo}"}
	if _, err := byo.SessionPlanForProviderWithCoder("claude-code", ProviderCoder, bad, &override); err == nil {
		t.Fatal("an unresolved {repo} was stamped")
	}
}

// Decoding keeps the checkout only for the dedicated worker user, accepts a
// profile written before the field existed, and refuses an unsafe value.
func TestDecodeCoderSessionProfileCheckoutDirectory(t *testing.T) {
	decode := func(extra string) (CoderSessionProfile, error) {
		return DecodeCoderSessionProfile(json.RawMessage(`{"coder":{"baseUrl":"https://coder.example.com","owner":"o",` +
			`"templateId":"t","durableRoot":"$HOME"` + extra + `}}`))
	}
	profile, err := decode(`,"checkoutDirectory":"~/work/alice","checkoutSiblings":["billing"]`)
	if err != nil || profile.CheckoutDirectory != "~/work/alice" || !slices.Equal(profile.CheckoutSiblings, []string{"billing"}) {
		t.Fatalf("profile = %+v, %v", profile, err)
	}
	legacy, err := decode(``)
	if err != nil || legacy.CheckoutDirectory != "" || legacy.CheckoutSiblings != nil {
		t.Fatalf("legacy profile = %+v, %v", legacy, err)
	}
	workspace, err := decode(`,"workerUser":"workspace","workingDirectory":"~/work/{repo}","checkoutDirectory":"~/work/alice"`)
	if err != nil || workspace.CheckoutDirectory != "" || workspace.WorkingDirectory != "~/work/{repo}" {
		t.Fatalf("workspace-user profile = %+v, %v", workspace, err)
	}
	for _, extra := range []string{
		`,"checkoutDirectory":"work/alice"`,
		`,"checkoutDirectory":"~/work/{repo}"`,
		`,"checkoutDirectory":"~/work/../alice"`,
		`,"checkoutDirectory":"~/work/alice","checkoutSiblings":["../etc"]`,
		`,"checkoutDirectory":"~/work/alice","checkoutSiblings":[".ao"]`,
	} {
		if profile, err := decode(extra); err == nil {
			t.Errorf("decode(%s) = %+v, want an error", extra, profile)
		}
	}
}
