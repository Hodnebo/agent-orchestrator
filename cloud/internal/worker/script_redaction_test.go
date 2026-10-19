package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/activity"
)

// scriptLeakCase is output a Setup or Cleanup step may print with a
// credential in it. secrets are the substrings that must never be published.
type scriptLeakCase struct {
	name    string
	text    string
	secrets []string
}

func scriptLeakCases() []scriptLeakCase {
	privateKey := "-----BEGIN RSA PRIVATE KEY-----\n" +
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7Leak14BodyA\n" +
		"VJTUt9Us8cKjMzEfYyjiWA4R4key=Leak14BodyB/M2bS1GB4t7NXp98C3SC6d==\n" +
		"-----END RSA PRIVATE KEY-----"
	return []scriptLeakCase{
		{"secrets manager", `{"SecretString":"{\"password\":\"hunter2xyz\"}"}`, []string{"hunter2xyz"}},
		{"secrets manager spaced", `"SecretString": "{\"password\": \"Leak01spc\"}",`, []string{"Leak01spc"}},
		{"double escaped", `{\"SecretString\":\"{\\\"password\\\":\\\"Leak02dbl\\\"}\"}`, []string{"Leak02dbl"}},
		{"double escaped, plain wrapper", `{"Data":"{\"Inner\":\"{\\\"password\\\":\\\"Leak02wrap\\\"}\"}"}`, []string{"Leak02wrap"}},
		{"escaped, plain wrapper", `{"Data":"{\"db_password\":\"Leak02esc\",\"user\":\"u\"}"}`, []string{"Leak02esc"}},
		{"single quoted", `{'password': 'Leak03sq'}`, []string{"Leak03sq"}},
		{"colon", `password: Leak04col`, []string{"Leak04col"}},
		{"env", `PASSWORD=Leak05env`, []string{"Leak05env"}},
		{"export", `export DB_PASSWORD="Leak06exp"`, []string{"Leak06exp"}},
		{"flag", `mysql --password Leak07flag -h db`, []string{"Leak07flag"}},
		{"flag quoted", `deploy --api-token "Leak07q" --verbose`, []string{"Leak07q"}},
		{"flag equals", `mysql --password=Leak08eq -h db`, []string{"Leak08eq"}},
		{"url", `DATABASE_URL=postgres://u:Leak09url@h/db`, []string{"Leak09url"}},
		{"bearer", `Authorization: Bearer Leak10tok`, []string{"Leak10tok"}},
		{"bearer curl", `curl -H 'Authorization: Bearer Leak10crl' https://api`, []string{"Leak10crl"}},
		{"bearer pair", `auth_header=Bearer Leak10pr`, []string{"Leak10pr"}},
		{"json token", `{ "token": "Leak11tok" }`, []string{"Leak11tok"}},
		{"json token spaced colon", `{ "token" : "Leak11spc" }`, []string{"Leak11spc"}},
		{"yaml quoted", `  password: "Leak12yml"`, []string{"Leak12yml"}},
		{"secrets manager multiple keys",
			`"SecretString": "{\"username\":\"admin\",\"password\":\"Leak13pw\",\"api_key\":\"Leak13key\",\"host\":\"db\"}",`,
			[]string{"Leak13pw", "Leak13key"}},
		{"secrets manager compact multiple keys",
			`{"SecretString":"{\"api_key\":\"Leak13ck\",\"password\":\"Leak13cp\"}","Name":"prod/db"}`,
			[]string{"Leak13ck", "Leak13cp"}},
		{"private key", privateKey, []string{"Leak14BodyA", "Leak14BodyB"}},
		{"escaped quote in value", `{"password":"Le\"ak15esc"}`, []string{"ak15esc"}},
		{"value ending in a backslash", `{"password":"Leak16bs\\","user":"u"}`, []string{"Leak16bs"}},
		{"single quoted with a blank", `DB_PASSWORD='Leak18 spaced'`, []string{"Leak18", "spaced"}},
		{"unterminated escaped", `{"SecretString":"{\"password\":\"Leak19open`, []string{"Leak19open"}},
		// What a worker from before the fix published: the control plane
		// masks the rest.
		{"already cut short", `{"SecretString":[REDACTED]password\":\"Leak20old\"}"}`, []string{"Leak20old"}},
		// Short flags of commands that take a password.
		{"docker login -p", `docker login -u ci -p Leak30QZX registry.example`, []string{"Leak30QZX"}},
		{"docker login -p first", `+ sudo docker login -p "Leak31QZX" -u ci registry.example`, []string{"Leak31QZX"}},
		{"mysql -p attached", `mysql -uroot -pLeak32QZX -h db`, []string{"Leak32QZX"}},
		{"mariadb -p attached quoted", `mariadb -u root '-pLeak33QZX' app`, []string{"Leak33QZX"}},
		{"mysql -p separate", `mysql -u root -p Leak34QZX -e 'select 1'`, []string{"Leak34QZX"}},
		{"sshpass -p", `sshpass -p Leak35QZX ssh -p 22 deploy@host`, []string{"Leak35QZX"}},
		{"sshpass -p attached", `sshpass -pLeak36QZX scp f host:`, []string{"Leak36QZX"}},
		{"redis-cli -a", `redis-cli -h cache -a Leak37QZX ping`, []string{"Leak37QZX"}},
		{"curl -u", `curl -sSf -u admin:Leak38QZX https://api.example`, []string{"Leak38QZX"}},
		{"curl -u attached", `curl -uadmin:Leak39QZX https://api.example`, []string{"Leak39QZX"}},
		{"curl --user", `curl --user "admin:Leak40QZX" https://api.example`, []string{"Leak40QZX"}},
		{"curl combined -u", `curl -fsSu admin:Leak41QZX https://api.example`, []string{"Leak41QZX"}},
		// URLs.
		{"token as a URL's user", `git clone https://Leak42tok0123456789abcdef@github.com/o/r.git`, []string{"Leak42tok"}},
		{"github token as a URL's user", `remote: https://ghp_Leak43abcdefghijklmnopqrstuvwxyz0123@github.com/o/r`, []string{"Leak43"}},
		{"azure sas", `https://acct.blob.core.windows.net/c/b.tgz?sv=2022-11-02&se=2026-10-20&sp=r&sig=Leak44sig%2Fab%3D`, []string{"Leak44sig"}},
		{"sas sig first", `sig=Leak45sig%3D&sv=2022-11-02&sr=b`, []string{"Leak45sig"}},
		// A Kubernetes Secret, whatever its keys are named, in kubectl's
		// key order (data before kind) and with stringData after kind.
		{"kubernetes secret data", "apiVersion: v1\ndata:\n  dbhost: TGVhazQ2ZGF0YQ==\n  conn: TGVhazQ2Y29ubg==\nkind: Secret\nmetadata:\n  name: db\ntype: Opaque",
			[]string{"TGVhazQ2ZGF0YQ", "TGVhazQ2Y29ubg"}},
		{"kubernetes secret stringData", "---\nkind: Secret\napiVersion: v1\nstringData:\n  url: Leak47strdata\n  config: |\n    host: db\n    Leak47block\nmetadata:\n  name: x",
			[]string{"Leak47strdata", "Leak47block"}},
		{"kubernetes secret list", "items:\n- apiVersion: v1\n  data:\n    a: TGVhazQ4bGlzdA==\n  kind: Secret",
			[]string{"TGVhazQ4bGlzdA"}},
		// Argument lists.
		{"json argument list", `"Args": ["--token","Leak49arr","--verbose"]`, []string{"Leak49arr"}},
		{"json argument list spaced", `["deploy", "--password", "Leak50arr"]`, []string{"Leak50arr"}},
		{"assignment of flags", `ARGS=--password Leak51args`, []string{"Leak51args"}},
		{"bracketed flags", `usage seen: [--password Leak52brk]`, []string{"Leak52brk"}},
		// YAML block scalars.
		{"yaml literal block", "private_key: |\n  Leak53blockA\n  Leak53blockB\nnext: ok", []string{"Leak53blockA", "Leak53blockB"}},
		{"yaml folded block", "db:\n  password: >-\n    Leak54fold\n    Leak54fold2\n  host: db", []string{"Leak54fold"}},
		// Token shapes.
		{"hugging face", `HF said hf_Leak55abcdefghijklmnopqrstuvwxyz12`, []string{"Leak55"}},
		{"stripe restricted", `using rk_live_Leak56abcdefghijklmn`, []string{"Leak56"}},
		{"stripe secret", `using sk_live_Leak57abcdefghijklmn`, []string{"Leak57"}},
		// Values holding a separator or a quote.
		{"value with a semicolon", `DB_PASSWORD=Leak58;B58semi`, []string{"Leak58", "B58semi"}},
		{"value with a comma", `API_TOKEN=Leak59,B59comma`, []string{"Leak59", "B59comma"}},
		{"value with an ampersand", `client_secret=Leak60&B60amp`, []string{"Leak60", "B60amp"}},
		{"value with a quote", `PASSWORD=Leak61"B61quote`, []string{"Leak61", "B61quote"}},
		{"quoted value with separators", `PASSWORD="Leak62;a,b&c" next`, []string{"Leak62", "a,b&c"}},
		// Escapes between the name and the value.
		{"colour codes", "\x1b[1mNPM_AUTH\x1b[0m=\x1b[32mLeak63ansi\x1b[0m", []string{"Leak63ansi"}},
		{"colour code after the separator", "SIGNING_KEY:\x1b[33m Leak64ansi\x1b[0m", []string{"Leak64ansi"}},
		// Python strings.
		{"python unicode string", `{'password': u'Leak65py', 'user': u'x'}`, []string{"Leak65py"}},
		{"python bytes string", `{"api_key": b"Leak66py"}`, []string{"Leak66py"}},
	}
}

// assertNoLeak fails when published holds any of the case's secrets.
func assertNoLeak(t *testing.T, path string, test scriptLeakCase, published string) {
	t.Helper()
	for _, secret := range test.secrets {
		if strings.Contains(published, secret) {
			t.Errorf("%s: %s published %q from %q", test.name, path, published, test.text)
		}
	}
	if !strings.Contains(published, "[REDACTED]") {
		t.Errorf("%s: %s published %q without a redaction marker", test.name, path, published)
	}
}

// No credential in a step's output or command leaves the sandbox or is kept
// by the control plane, through any of the production paths: the worker's
// final and live step output, the control plane's sanitizing of a worker's
// event, the Cleanup hook's output parsed into steps (with and without
// markers), and the script redaction the Setup summary and startup error get.
func TestScriptOutputNeverLeaksASecret(t *testing.T) {
	t.Parallel()
	redactor := newScriptRedactor(nil, nil)
	for _, test := range scriptLeakCases() {
		paths := map[string]string{"RedactScriptOutput": RedactScriptOutput(test.text)}
		paths["scriptStepOutput"], _ = redactor.scriptStepOutput(test.text+"\n", false)
		paths["scriptStepOutput, unfinished line"], _ = redactor.scriptStepOutput(test.text, false)
		paths["liveStepOutput"], _ = redactor.liveStepOutput("started\n"+test.text+"\n", false)
		paths["SanitizeScriptStepEvent output"] = SanitizeScriptStepEvent(ScriptStepEvent{Output: test.text}).Output
		if !strings.Contains(test.text, "\n") {
			paths["SanitizeScriptStepEvent command"] = SanitizeScriptStepEvent(ScriptStepEvent{Command: "echo " + test.text}).Command
			command, _ := scriptStepCommand("echo "+test.text, redactor)
			paths["scriptStepCommand"] = command
		}
		// The worker's event, sanitized again by the control plane.
		workerOutput, _ := redactor.scriptStepOutput(test.text+"\n", false)
		paths["worker then control plane"] = SanitizeScriptStepEvent(ScriptStepEvent{Output: workerOutput}).Output

		nonce := NewScriptMarkerNonce()
		marked := scriptMarkerPrefix + nonce + " 1 1 start 1700000000000 / echo\n" + test.text + "\n" +
			scriptMarkerPrefix + nonce + " 1 1 end 0 5\n"
		events := CleanupStepEvents(marked, CleanupRun{RunID: "r", Nonce: nonce, StartedAt: time.Now()})
		if len(events) != 1 {
			t.Fatalf("%s: cleanup events = %+v", test.name, events)
		}
		paths["CleanupStepEvents"] = events[0].Output
		events = CleanupStepEvents("cleaning\n"+test.text+"\n", CleanupRun{RunID: "r", ExitCode: 1, StartedAt: time.Now()})
		if len(events) != 1 {
			t.Fatalf("%s: cleanup events without markers = %+v", test.name, events)
		}
		paths["CleanupStepEvents without markers"] = events[0].Output

		for path, published := range paths {
			assertNoLeak(t, path, test, published)
		}
		encoded, _ := json.Marshal(paths)
		for _, secret := range test.secrets {
			if strings.Contains(string(encoded), secret) {
				t.Errorf("%s: a path published %q", test.name, secret)
			}
		}
	}
}

// End to end: a Setup step that prints every case publishes none of them in
// its live or final events, and the failed run's summary (what the startup
// screen shows) holds none, as the run returns it and as the worker redacts it
// again before sending it. A secret environment variable's value is masked
// even where nothing names it: printed bare, traced by set -x, and as the
// unfinished last line.
func TestSetupRunNeverPublishesASecret(t *testing.T) {
	t.Setenv("MY_SERVICE_SECRET", "LeakEnvSvc01x")
	t.Setenv("REGISTRY_PASSWORD", "LeakEnvReg02x")
	cases := append(scriptLeakCases(),
		scriptLeakCase{name: "env value, bare", secrets: []string{"LeakEnvSvc01x"}},
		scriptLeakCase{name: "env value, set -x", secrets: []string{"LeakEnvReg02x"}},
	)
	var printed strings.Builder
	for _, test := range cases {
		printed.WriteString(test.text + "\n")
	}
	file := filepath.Join(t.TempDir(), "output.txt")
	if err := os.WriteFile(file, []byte(printed.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, events := runSetupWithSteps(t, SetupRunner{UpdateInterval: 10 * time.Millisecond},
		[]string{"cat " + file + `; printf '%s\n' "$MY_SERVICE_SECRET"; ` +
			`(set -x; : docker login -u ci -p "$REGISTRY_PASSWORD" registry.example) 2>&1; ` +
			`sleep 0.1; printf 'last %s' "$MY_SERVICE_SECRET"; exit 1`})
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	// JSON escapes the published text's quotes and backslashes, not the
	// secrets, which are letters and digits.
	published := string(encoded) + "\n" + summary.Output + "\n" + summary.Command + "\n" +
		RedactScriptOutput(summary.Output) + "\n" + RedactScriptOutput(summary.Message())
	for _, test := range cases {
		for _, secret := range test.secrets {
			if strings.Contains(published, secret) {
				t.Errorf("%s: %q published", test.name, secret)
			}
		}
	}
}

// leaksPart reports whether published holds any 4-byte piece of secret.
func leaksPart(published, secret string) bool {
	for start := 0; start+4 <= len(secret); start++ {
		if strings.Contains(published, secret[start:start+4]) {
			return true
		}
	}
	return false
}

// A secret where the output is cut is never published in part: at the 16 KiB
// tail an event carries (redacted before it is cut), where the raw tail kept
// for a step begins, and where the Setup summary's and the Cleanup hook's
// tails begin.
func TestScriptOutputCutNeverSplitsASecret(t *testing.T) {
	t.Parallel()
	redactor := newScriptRedactor(nil, nil)
	const secret = "Leak21CutBoundary"
	line := "DB_PASSWORD=" + secret
	for shift := 0; shift <= len(line); shift++ {
		// The secret's line straddles the 16 KiB tail an event carries, on
		// its own line and inside one long line.
		after := ScriptStepOutputLimit - len(line) + shift
		for _, text := range []string{
			strings.Repeat("filler line\n", ScriptStepOutputLimit/12+2) + line + "\n" + strings.Repeat("z", after) + "\n",
			strings.Repeat("x", ScriptStepOutputLimit) + " " + line + " " + strings.Repeat("y", after) + "\n",
		} {
			output, _ := redactor.scriptStepOutput(text, false)
			sanitized := SanitizeScriptStepEvent(ScriptStepEvent{Output: text}).Output
			fitted := fitScriptStepEvent(ScriptStepEvent{Phase: ScriptPhaseSetup, RunID: "r", Step: 1, Total: 1, Output: output}).Output
			for _, published := range []string{output, sanitized, fitted} {
				if leaksPart(published, secret) {
					t.Fatalf("shift %d: 16 KiB tail published part of the secret", shift)
				}
			}
		}
		// The raw tail kept for a running step starts shift bytes into the
		// secret's line.
		tail := &liveTail{limit: scriptLiveTailLimit}
		_, _ = tail.Write([]byte("start\n" + line + "\n"))
		_, _ = tail.Write([]byte(strings.Repeat("b", scriptLiveTailLimit-len(line)-2+shift) + "\n"))
		raw, dropped, _ := tail.snapshot()
		if shift > 0 && (!dropped || !strings.HasPrefix(raw, line[shift:])) {
			t.Fatalf("shift %d: raw tail starts %q (dropped %v)", shift, raw[:min(len(raw), 30)], dropped)
		}
		final, _ := redactor.scriptStepOutput(raw, dropped)
		live, _ := redactor.liveStepOutput(raw, dropped)
		if leaksPart(final, secret) || leaksPart(live, secret) {
			t.Fatalf("shift %d: raw tail cut published part of the secret", shift)
		}
		// The Setup summary's tail (worker side, before the script redaction).
		buffer := &tailBuffer{limit: len(line) + 66 - shift}
		_, _ = buffer.Write([]byte(line + "\n" + strings.Repeat("d", 64) + "\n"))
		if summary := RedactScriptOutput(buffer.String()); leaksPart(summary, secret) {
			t.Fatalf("shift %d: setup summary published %q", shift, summary)
		}
		// A Cleanup hook's tail without markers, cut shift bytes into the line.
		hookOutput := line[shift:] + "\n" + strings.Repeat("e\n", CleanupHookOutputLimit/2)
		events := CleanupStepEvents(hookOutput, CleanupRun{RunID: "r", ExitCode: 1, StartedAt: time.Now()})
		if len(events) != 1 || leaksPart(events[0].Output, secret) {
			t.Fatalf("shift %d: cleanup hook tail published part of the secret", shift)
		}
	}
}

// A secret printed across two live snapshots (the step wrote part of the line,
// then the rest) is never published in part: an unfinished line waits for its
// newline, and the whole line is then masked.
func TestScriptOutputSecretSplitAcrossLiveSnapshots(t *testing.T) {
	t.Parallel()
	redactor := newScriptRedactor(nil, nil)
	for index, test := range scriptLeakCases() {
		if strings.Contains(test.text, "\n") {
			continue
		}
		heading := "step " + strconv.Itoa(index)
		tail := &liveTail{limit: scriptLiveTailLimit}
		half := len(test.text) / 2
		_, _ = tail.Write([]byte(heading + "\n" + test.text[:half]))
		raw, dropped, _ := tail.snapshot()
		if first, _ := redactor.liveStepOutput(raw, dropped); first != heading {
			t.Errorf("%s: first snapshot = %q; the unfinished line must wait", test.name, first)
		}
		_, _ = tail.Write([]byte(test.text[half:] + "\n"))
		raw, dropped, _ = tail.snapshot()
		second, _ := redactor.liveStepOutput(raw, dropped)
		for _, secret := range test.secrets {
			if leaksPart(second, secret) {
				t.Errorf("%s: second snapshot published %q", test.name, second)
			}
		}
	}
}

// The redaction stays linear in its input: each worst case below, 64 KiB of
// what makes one of its passes look back or ahead (a value, block, command or
// document that never ends, pairs packed onto one line), goes through the
// passes that read the text with their own loops (the regular expressions
// alone, activity.Redact's and the token shapes, are linear by construction)
// within a bound, 30 times what plain text of that size takes and at least
// 1 s, that a pass reading the text again per match (quadratic: billions of
// steps at this size) far exceeds. It runs alone, not in parallel, so the
// timings compare.
func TestScriptRedactionIsLinear(t *testing.T) {
	const size = scriptLiveTailLimit
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)+1)[:size] }
	cases := map[string]string{
		"names without separators": fill("token"),
		"pairs":                    fill("password="),
		"pairs to the blank":       fill("password=x;"),
		"unclosed quotes":          fill(`password: "`),
		"block headers on a line":  fill("password: | #") + "\n" + fill(" ")[:size/2] + "\nnext: 1",
		"block headers":            fill("password: |\n"),
		"nested blocks":            fill("password: |\n  "),
		"escaped quotes":           `"token":"` + fill(`\\\"`),
		"backslashes":              `"token":"` + fill(`\`),
		"escaped members":          fill(`\"password\":\"`),
		"single quotes":            fill(`password='`),
		"flags":                    fill("--token "),
		"flags to flags":           fill("-token -"),
		"argument lists":           fill(`"--token",`),
		"python strings":           fill(`password: u'`),
		"commands":                 fill("mysql -p "),
		"logins":                   fill("docker login -p x "),
		"command words":            fill("docker "),
		"sshpass words":            fill("sshpass "),
		"quoted arguments":         "mysql " + fill(`"-p`),
		"curl users":               fill("curl -u a:b "),
		"secret documents":         fill("kind: Secret\ndata:\n  a: b\n"),
		"secret data":              "kind: Secret\ndata:\n" + fill("  a: |\n"),
		"secret documents split":   fill("kind: Secret\n---\ndata:\n"),
		"url users":                fill("https://abcdefghij0123456789@"),
		"url schemes":              fill("a://"),
		"signatures":               fill("&sig="),
		"escapes":                  fill("\x1b["),
		"carriage returns":         fill("password=x\r"),
		"bearer":                   fill("token: Bearer "),
	}
	redactor := newScriptRedactor([]string{"MY_SERVICE_SECRET=value-of-a-secret"}, []string{"ao_worker.token.value"})
	redactAll := func(text string) time.Duration {
		started := time.Now()
		text = activity.StripControl(collapseCarriageReturns(text))
		text = maskURLTokens(maskKubernetesSecretData(maskCommandPasswords(redactSecretPairs(text))))
		_ = redactor.maskTrailingFragment(text)
		return time.Since(started)
	}
	bound := max(time.Second, 30*redactAll(fill("plain text ")))
	for name, text := range cases {
		if elapsed := redactAll(text); elapsed > bound {
			t.Errorf("%s: redacting 64 KiB took %v (bound %v)", name, elapsed, bound)
		}
	}
}
