package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	searchsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/search"
)

type fakeSearch struct {
	got  searchsvc.Request
	resp searchsvc.Response
	err  error
}

func (f *fakeSearch) Search(_ context.Context, req searchsvc.Request) (searchsvc.Response, error) {
	f.got = req
	return f.resp, f.err
}

func newSearchRig(t *testing.T, svc SearchService) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	(&SearchController{Svc: svc}).Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func getSearch(t *testing.T, srv *httptest.Server, params url.Values) (int, SearchResponse, envelope.APIError) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/search?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body SearchResponse
	var apiErr envelope.APIError
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
	} else if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, apiErr
}

func TestSearchControllerPassesRequestAndShapesResponse(t *testing.T) {
	created := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	svc := &fakeSearch{resp: searchsvc.Response{
		FileNames: []searchsvc.FileNameHit{{Path: "src/palette.ts", Score: 9, ExactName: true}},
		FileContent: []searchsvc.ContentHit{{Path: "src/palette.ts", Score: 5, Decisive: true,
			Snippet: searchsvc.Snippet{Text: "close the palette", Line: 12, Highlights: [][2]int{{10, 17}}}}},
		Messages: []searchsvc.MessageHit{{MessageID: "m1", ConversationID: "c1", SessionID: "s1", Scope: domain.ConversationScopeSession,
			ConversationTitle: "Palette", Role: domain.MessageRoleUser, CreatedAt: created, Score: 4, OlderVersion: true,
			// "é" is two bytes but one UTF-16 unit; "😀" is four bytes and two units.
			Snippet: searchsvc.Snippet{Text: "é😀 palette", Highlights: [][2]int{{7, 14}}}}},
		Files: searchsvc.IndexStatus{State: searchsvc.IndexReady, Progress: 1},
		Chats: searchsvc.IndexStatus{State: searchsvc.IndexBuilding, Progress: 0.4},
	}}
	srv := newSearchRig(t, svc)
	code, body, _ := getSearch(t, srv, url.Values{"q": {"palette"}, "sessionId": {" s1 "}, "kinds": {"fileNames,messages"}, "limit": {"5"}})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if svc.got.Query != "palette" || svc.got.SessionID != "s1" || svc.got.Limit != 5 ||
		!svc.got.Kinds.FileNames || svc.got.Kinds.FileContent || !svc.got.Kinds.Messages {
		t.Fatalf("request = %+v", svc.got)
	}
	if body.Query != "palette" || len(body.FileNames) != 1 || !body.FileNames[0].ExactName || body.Files.State != "ready" || body.Chats.Progress != 0.4 {
		t.Fatalf("body = %+v", body)
	}
	c := body.FileContent[0]
	if c.Snippet.Line != 12 || !c.Decisive || c.Snippet.Highlights[0] != (SearchHighlight{Start: 10, End: 17}) {
		t.Fatalf("content hit = %+v", c)
	}
	m := body.Messages[0]
	if m.MessageID != "m1" || !m.OlderVersion || !m.CreatedAt.Equal(created) {
		t.Fatalf("message hit = %+v", m)
	}
	if h := m.Snippet.Highlights[0]; h != (SearchHighlight{Start: 4, End: 11}) {
		t.Fatalf("UTF-16 highlight = %+v, want {4 11}", h)
	}
}

func TestSearchControllerDefaultsToAllKinds(t *testing.T) {
	svc := &fakeSearch{}
	srv := newSearchRig(t, svc)
	code, body, _ := getSearch(t, srv, url.Values{"q": {"x"}})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !svc.got.Kinds.FileNames || !svc.got.Kinds.FileContent || !svc.got.Kinds.Messages || svc.got.Limit != 0 {
		t.Fatalf("request = %+v", svc.got)
	}
	// Empty groups are arrays, never null.
	if body.FileNames == nil || body.FileContent == nil || body.Messages == nil {
		t.Fatalf("nil groups in %+v", body)
	}
}

func TestSearchControllerRejectsBadParameters(t *testing.T) {
	srv := newSearchRig(t, &fakeSearch{})
	for _, tc := range []struct {
		params url.Values
		code   string
	}{
		{url.Values{"q": {"x"}, "kinds": {"files"}}, "INVALID_SEARCH_KINDS"},
		{url.Values{"q": {"x"}, "limit": {"0"}}, "INVALID_SEARCH_LIMIT"},
		{url.Values{"q": {"x"}, "limit": {"51"}}, "INVALID_SEARCH_LIMIT"},
		{url.Values{"q": {"x"}, "limit": {"many"}}, "INVALID_SEARCH_LIMIT"},
	} {
		status, _, apiErr := getSearch(t, srv, tc.params)
		if status != http.StatusBadRequest || apiErr.Code != tc.code {
			t.Errorf("%v: status=%d code=%q, want 400 %s", tc.params, status, apiErr.Code, tc.code)
		}
	}
}

func TestSearchControllerSurfacesServiceErrors(t *testing.T) {
	srv := newSearchRig(t, &fakeSearch{err: apierr.Invalid("SEARCH_QUERY_REQUIRED", "query is required", nil)})
	status, _, apiErr := getSearch(t, srv, url.Values{"q": {""}})
	if status != http.StatusBadRequest || apiErr.Code != "SEARCH_QUERY_REQUIRED" {
		t.Fatalf("status=%d code=%q", status, apiErr.Code)
	}
}

func TestSearchControllerNotImplementedWithoutService(t *testing.T) {
	srv := newSearchRig(t, nil)
	status, _, _ := getSearch(t, srv, url.Values{"q": {"x"}})
	if status != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", status)
	}
}
