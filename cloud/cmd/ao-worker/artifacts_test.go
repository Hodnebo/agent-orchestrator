package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

type fakeArtifactRow struct {
	size      int64
	sha       string
	versionID string
}

// fakeArtifactPlane implements the three worker artifact routes in memory.
type fakeArtifactPlane struct {
	mu          sync.Mutex
	rows        map[string]fakeArtifactRow
	versions    map[string]string
	manifests   int
	uploads     int
	commits     int
	failCommits int
	// refuseCommits answers every commit with this status when set.
	refuseCommits int
	// staleCommits answers every commit as from a replaced worker.
	staleCommits bool
	// rejectUploads answers that many uploads with 422.
	rejectUploads int
	disabled      bool
	deleted       []string
	nextVersion   int
}

func newFakeArtifactPlane(t *testing.T) (*fakeArtifactPlane, *client) {
	t.Helper()
	plane := &fakeArtifactPlane{rows: map[string]fakeArtifactRow{}, versions: map[string]string{}}
	server := httptest.NewServer(plane)
	t.Cleanup(server.Close)
	return plane, &client{baseURL: server.URL, http: server.Client(), token: "worker-token"}
}

func (p *fakeArtifactPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disabled {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Worker worker-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == artifactManifestPath:
		p.manifests++
		var input worker.ArtifactManifestRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		changed := []string{}
		for _, file := range input.Files {
			row, ok := p.rows[file.Path]
			if !ok || row.size != file.Size || row.sha != file.SHA256 {
				changed = append(changed, file.Path)
			}
		}
		_ = json.NewEncoder(w).Encode(worker.ArtifactManifestResponse{Changed: changed})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, artifactBlobPath):
		path, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), artifactBlobPath))
		body, _ := io.ReadAll(r.Body)
		if p.rejectUploads > 0 {
			p.rejectUploads--
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != r.Header.Get(worker.ArtifactSHA256Header) || int64(len(body)) != r.ContentLength {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		p.uploads++
		p.nextVersion++
		version := "v" + string(rune('0'+p.nextVersion))
		p.versions[version] = hex.EncodeToString(sum[:])
		_ = json.NewEncoder(w).Encode(worker.ArtifactBlobResponse{Path: path, VersionID: version})
	case r.Method == http.MethodPost && r.URL.Path == artifactCommitPath:
		var input worker.ArtifactCommitRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		if p.failCommits > 0 {
			p.failCommits--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if p.refuseCommits != 0 {
			w.WriteHeader(p.refuseCommits)
			return
		}
		if p.staleCommits {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"STALE_WORKER_TOKEN"}`))
			return
		}
		for _, file := range input.Files {
			if p.versions[file.VersionID] != file.SHA256 {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		p.commits++
		for _, file := range input.Files {
			p.rows[file.Path] = fakeArtifactRow{size: file.Size, sha: file.SHA256, versionID: file.VersionID}
		}
		for _, path := range input.Deleted {
			delete(p.rows, path)
			p.deleted = append(p.deleted, path)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (p *fakeArtifactPlane) paths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, 0, len(p.rows))
	for path := range p.rows {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (p *fakeArtifactPlane) counts() (manifests, uploads, commits int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifests, p.uploads, p.commits
}

func writeArtifact(t *testing.T, dir, path, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testSyncer(c *client, dir string) *artifactSyncer {
	return newArtifactSyncer(c, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// syncSettled runs the two scans a new or changed file needs.
func syncSettled(s *artifactSyncer) {
	s.sync(context.Background())
	s.sync(context.Background())
}

func TestArtifactSyncWaitsForFilesToSettle(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	s := testSyncer(c, dir)
	writeArtifact(t, dir, "report.html", "<h1>draft</h1>")

	if changing := s.sync(context.Background()); !changing {
		t.Fatal("a new file was not reported as changing")
	}
	if manifests, uploads, _ := plane.counts(); manifests != 0 || uploads != 0 {
		t.Fatalf("first scan sent requests: manifests=%d uploads=%d", manifests, uploads)
	}
	// A file still being written stays unsettled.
	writeArtifact(t, dir, "report.html", "<h1>draft, longer</h1>")
	s.sync(context.Background())
	if _, uploads, _ := plane.counts(); uploads != 0 {
		t.Fatal("a file that changed between scans was uploaded")
	}
	if changing := s.sync(context.Background()); changing {
		t.Fatal("a settled file was reported as changing")
	}
	if got := plane.paths(); !slices.Equal(got, []string{"report.html"}) {
		t.Fatalf("committed = %v", got)
	}
}

func TestArtifactSyncSkipsSymlinksVersionsAndOversizedFiles(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "linked-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeArtifact(t, dir, ".versions/report.html/1.html", "old")
	writeArtifact(t, dir, "nested/.versions/x.md", "old")
	writeArtifact(t, dir, "report.html", "current")
	big, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(26 << 20); err != nil {
		t.Fatal(err)
	}
	_ = big.Close()

	syncSettled(testSyncer(c, dir))
	if got := plane.paths(); !slices.Equal(got, []string{"report.html"}) {
		t.Fatalf("committed = %v, want only report.html", got)
	}
}

func TestArtifactSyncSendsNoUploadForUnchangedFiles(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "a.md", "a")
	writeArtifact(t, dir, "assets/b.png", "b")
	s := testSyncer(c, dir)
	syncSettled(s)
	manifests, uploads, commits := plane.counts()
	if uploads != 2 || commits != 1 {
		t.Fatalf("first sync uploads=%d commits=%d", uploads, commits)
	}
	s.sync(context.Background())
	if m, u, c := plane.counts(); m != manifests || u != uploads || c != commits {
		t.Fatalf("an unchanged scan made requests: manifests %d->%d uploads %d->%d commits %d->%d",
			manifests, m, uploads, u, commits, c)
	}

	// A restarted worker with the same files asks once and uploads nothing.
	restarted := testSyncer(c, dir)
	syncSettled(restarted)
	if _, u, c := plane.counts(); u != uploads || c != commits {
		t.Fatalf("restart re-uploaded: uploads %d->%d commits %d->%d", uploads, u, commits, c)
	}
}

func TestArtifactSyncDeletesOnlyFilesItSynced(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "keep.md", "keep")
	writeArtifact(t, dir, "drop.md", "drop")
	s := testSyncer(c, dir)
	syncSettled(s)

	if err := os.Remove(filepath.Join(dir, "drop.md")); err != nil {
		t.Fatal(err)
	}
	s.sync(context.Background())
	if got := plane.paths(); !slices.Equal(got, []string{"keep.md"}) {
		t.Fatalf("after delete committed = %v", got)
	}

	// A restore onto a fresh disk starts with an empty directory: nothing is
	// deleted from the stored list.
	fresh := testSyncer(c, t.TempDir())
	syncSettled(fresh)
	if got := plane.paths(); !slices.Equal(got, []string{"keep.md"}) {
		t.Fatalf("fresh disk changed the stored list: %v", got)
	}
}

func TestArtifactSyncRetriesFailedCommitWithoutReuploading(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.failCommits = 1
	s := testSyncer(c, dir)
	syncSettled(s)
	if got := plane.paths(); len(got) != 0 {
		t.Fatalf("failed commit stored %v", got)
	}
	if s.failures != 1 || !s.retryAt.After(time.Now()) {
		t.Fatalf("failed commit: failures=%d retryAt=%v, want a backoff", s.failures, s.retryAt)
	}
	s.sync(context.Background())
	if _, _, commits := plane.counts(); commits != 0 {
		t.Fatal("a sync during the backoff retried the commit")
	}
	s.retryAt = time.Time{}
	if changing := s.sync(context.Background()); changing {
		t.Fatal("retry after a failed commit reported a change")
	}
	_, uploads, commits := plane.counts()
	if commits != 1 || uploads != 1 {
		t.Fatalf("retry uploads=%d commits=%d, want the first version reused", uploads, commits)
	}
	if got := plane.paths(); !slices.Equal(got, []string{"report.html"}) {
		t.Fatalf("committed = %v", got)
	}
}

func TestArtifactSyncStopsWhenControlPlaneHasNoArtifacts(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	plane.disabled = true
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	s := testSyncer(c, dir)
	syncSettled(s)
	if !s.disabledUntil.After(time.Now()) {
		t.Fatal("syncer kept running against a control plane without artifacts")
	}
}

func TestArtifactSyncFinalFlushOnShutdown(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	s := testSyncer(c, dir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	writeArtifact(t, dir, "last.md", "written just before shutdown")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(artifactFinalFlushTimeout):
		t.Fatal("final flush did not finish")
	}
	if got := plane.paths(); !slices.Equal(got, []string{"last.md"}) {
		t.Fatalf("final flush committed %v", got)
	}
}

func TestEscapeArtifactPath(t *testing.T) {
	if got := escapeArtifactPath("my docs/rep ort#1.html"); got != "my%20docs/rep%20ort%231.html" {
		t.Fatalf("escapeArtifactPath = %q", got)
	}
}

func TestArtifactSyncBacksOffAfterRepeatedFailures(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.failCommits = 3
	s := testSyncer(c, dir)
	syncSettled(s)
	var delays []time.Duration
	for range 2 {
		delays = append(delays, time.Until(s.retryAt).Round(time.Second))
		s.retryAt = time.Time{}
		if changing := s.sync(context.Background()); changing {
			t.Fatal("a failed request scheduled a settle rescan")
		}
	}
	if delays[0] != artifactRetryBase || delays[1] != 2*artifactRetryBase {
		t.Fatalf("backoff delays = %v, want %v then %v", delays, artifactRetryBase, 2*artifactRetryBase)
	}
	s.retryAt = time.Time{}
	s.sync(context.Background())
	if got := plane.paths(); !slices.Equal(got, []string{"report.html"}) || s.failures != 0 {
		t.Fatalf("after recovery committed = %v, failures = %d", got, s.failures)
	}
}

func TestArtifactSyncWaitsForFilesToChangeAfterARefusal(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.refuseCommits = http.StatusRequestEntityTooLarge
	s := testSyncer(c, dir)
	syncSettled(s)
	manifests, uploads, _ := plane.counts()
	s.sync(context.Background())
	s.sync(context.Background())
	if m, u, _ := plane.counts(); m != manifests || u != uploads {
		t.Fatalf("a refused commit was retried with the same files: manifests %d->%d uploads %d->%d", manifests, m, uploads, u)
	}
	plane.mu.Lock()
	plane.refuseCommits = 0
	plane.mu.Unlock()
	writeArtifact(t, dir, "notes.md", "notes")
	syncSettled(s)
	if got := plane.paths(); !slices.Equal(got, []string{"notes.md", "report.html"}) {
		t.Fatalf("after the files changed committed = %v", got)
	}
}

func TestArtifactSyncStopsForAReplacedWorker(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.staleCommits = true
	s := testSyncer(c, dir)
	syncSettled(s)
	if !s.disabledUntil.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("disabledUntil = %v, want the syncer stopped", s.disabledUntil)
	}
}

func TestArtifactSyncUploadsEmptyFiles(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "placeholder.md", "")
	syncSettled(testSyncer(c, dir))
	if got := plane.paths(); !slices.Equal(got, []string{"placeholder.md"}) {
		t.Fatalf("committed = %v, want the empty file", got)
	}
}

func TestArtifactSyncRehashesAfterAChecksumRejection(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.rejectUploads = 1
	s := testSyncer(c, dir)
	syncSettled(s)
	if state := s.previous["report.html"]; state.sha != "" {
		t.Fatalf("cached sha kept after a checksum rejection: %q", state.sha)
	}
	s.retryAt = time.Time{}
	syncSettled(s)
	if got := plane.paths(); !slices.Equal(got, []string{"report.html"}) {
		t.Fatalf("committed = %v", got)
	}
}

func TestArtifactSyncKeepsFilesItCannotSync(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.csv", "small")
	writeArtifact(t, dir, "locked/notes.md", "notes")
	s := testSyncer(c, dir)
	syncSettled(s)
	if got := plane.paths(); !slices.Equal(got, []string{"locked/notes.md", "report.csv"}) {
		t.Fatalf("committed = %v", got)
	}
	// The file grows past the per-file limit: its last good version stays.
	big, err := os.OpenFile(filepath.Join(dir, "report.csv"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(26 << 20); err != nil {
		t.Fatal(err)
	}
	_ = big.Close()
	// A directory that cannot be listed keeps its files.
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o700) })
	if _, err := os.ReadDir(filepath.Join(dir, "locked")); err == nil {
		t.Skip("directory permissions are not enforced here")
	}
	syncSettled(s)
	if got := plane.paths(); !slices.Equal(got, []string{"locked/notes.md", "report.csv"}) {
		t.Fatalf("files the syncer could not read were deleted: %v", got)
	}
}

func TestArtifactPathStorable(t *testing.T) {
	if !artifactPathStorable("docs/report.html") {
		t.Fatal("a short path is not storable")
	}
	if !artifactPathStorable(strings.Repeat("a", maxArtifactPathBytes)) {
		t.Fatal("a path at the key limit is not storable")
	}
	if artifactPathStorable(strings.Repeat("a", maxArtifactPathBytes+1)) {
		t.Fatal("a path over the key limit is storable")
	}
	if artifactPathStorable("CON.md") {
		t.Fatal("an invalid path is storable")
	}
}

func TestArtifactSyncSkipsToolDirectories(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, ".git/HEAD", "ref")
	writeArtifact(t, dir, "app/node_modules/x/index.js", "x")
	writeArtifact(t, dir, "report.html", "report")
	syncSettled(testSyncer(c, dir))
	if got := plane.paths(); !slices.Equal(got, []string{"report.html"}) {
		t.Fatalf("committed = %v, want only report.html", got)
	}
}

func TestArtifactSyncStopsRescanningAFileThatKeepsChanging(t *testing.T) {
	_, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	s := testSyncer(c, dir)
	changing := true
	for index := range artifactMaxUnsettledScans + 1 {
		writeArtifact(t, dir, "log.txt", strings.Repeat("x", index+1))
		changing = s.sync(context.Background())
	}
	if changing {
		t.Fatal("a file that keeps changing still schedules settle rescans")
	}
}

// At the file cap, a new file that sorts first does not push a synced file
// out: the synced files keep their slots and edits to them still sync.
func TestArtifactSyncKeepsSyncedFilesAtTheCap(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	for index := range domain.MaxSessionArtifactFiles {
		writeArtifact(t, dir, fmt.Sprintf("f%03d.md", index), "x")
	}
	s := testSyncer(c, dir)
	syncSettled(s)
	if got := len(plane.paths()); got != domain.MaxSessionArtifactFiles {
		t.Fatalf("synced %d files, want %d", got, domain.MaxSessionArtifactFiles)
	}
	writeArtifact(t, dir, "a.md", "new")
	writeArtifact(t, dir, "f199.md", "edited")
	syncSettled(s)
	paths := plane.paths()
	if slices.Contains(paths, "a.md") || !slices.Contains(paths, "f199.md") || len(paths) != domain.MaxSessionArtifactFiles {
		t.Fatalf("at the cap: %d files, a.md=%v, f199.md=%v", len(paths), slices.Contains(paths, "a.md"), slices.Contains(paths, "f199.md"))
	}
	plane.mu.Lock()
	edited := plane.rows["f199.md"].size
	plane.mu.Unlock()
	if edited != int64(len("edited")) {
		t.Fatalf("edit to a synced file at the cap was not synced: size %d", edited)
	}
}

func TestArtifactSyncRefusalIgnoresAFileThatKeepsChanging(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.refuseCommits = http.StatusRequestEntityTooLarge
	s := testSyncer(c, dir)
	syncSettled(s)
	manifests, _, _ := plane.counts()
	for index := range 4 {
		writeArtifact(t, dir, "progress.log", strings.Repeat("x", index+1))
		s.sync(context.Background())
	}
	if m, _, _ := plane.counts(); m != manifests {
		t.Fatalf("a growing log resent the refused request: manifests %d -> %d", manifests, m)
	}
}

func TestArtifactSyncRefusalIgnoresAnUnreadableFile(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	writeArtifact(t, dir, "secret.bin", "secret")
	if err := os.Chmod(filepath.Join(dir, "secret.bin"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "secret.bin"), 0o600) })
	if _, err := os.ReadFile(filepath.Join(dir, "secret.bin")); err == nil {
		t.Skip("file permissions are not enforced here")
	}
	plane.refuseCommits = http.StatusRequestEntityTooLarge
	s := testSyncer(c, dir)
	syncSettled(s)
	manifests, _, _ := plane.counts()
	for range 6 {
		s.sync(context.Background())
	}
	if m, _, _ := plane.counts(); m != manifests {
		t.Fatalf("an unreadable file resent the refused request: manifests %d -> %d", manifests, m)
	}
}

func TestArtifactSyncFinalFlushIgnoresTheBackoff(t *testing.T) {
	plane, c := newFakeArtifactPlane(t)
	dir := t.TempDir()
	writeArtifact(t, dir, "report.html", "report")
	plane.failCommits = 1
	s := testSyncer(c, dir)
	syncSettled(s)
	if !s.retryAt.After(time.Now()) {
		t.Fatal("no backoff after a failed commit")
	}
	writeArtifact(t, dir, "last.md", "written during the backoff")
	s.finalFlush()
	if got := plane.paths(); !slices.Equal(got, []string{"last.md", "report.html"}) {
		t.Fatalf("final flush during a backoff committed %v", got)
	}
}
