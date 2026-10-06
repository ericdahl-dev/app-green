package jira_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/model"
)

func TestMyTickets(t *testing.T) {
	var gotJQL string
	var gotFields []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JQL    string   `json:"jql"`
			Fields []string `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotJQL, gotFields = body.JQL, body.Fields
		http.ServeFile(w, r, "testdata/search.json")
	}))
	defer srv.Close()
	c := jira.New(srv.URL, "me@example.com", "tok")
	ts, _, err := c.MyTickets(context.Background(), []string{"ABC", "XYZ"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"assignee = currentUser()",
		"project in (ABC,XYZ)",
		"(statusCategory = 4 OR (statusCategory = 3 AND statusCategoryChangedDate >= -14d))",
		"ORDER BY updated DESC",
	} {
		if !strings.Contains(gotJQL, want) {
			t.Errorf("jql = %s, missing %q", gotJQL, want)
		}
	}
	if strings.Join(gotFields, ",") != "summary,status,updated,statuscategorychangedate" {
		t.Errorf("fields = %v", gotFields)
	}
	if len(ts) != 2 {
		t.Fatalf("got %d tickets, want 2: %+v", len(ts), ts)
	}
	edt := time.FixedZone("", -4*3600)
	want := model.Ticket{
		Key:            "ABC-1",
		Title:          "Add widget export",
		Status:         "Code Review",
		StatusCategory: model.StatusInProgress,
		URL:            srv.URL + "/browse/ABC-1",
		Updated:        time.Date(2026, 10, 5, 9, 0, 0, 0, edt),
		StatusSince:    time.Date(2026, 10, 1, 8, 30, 0, 0, edt),
	}
	if !ticketEqual(ts[0], want) {
		t.Errorf("ts[0] = %+v, want %+v", ts[0], want)
	}
	if ts[1].Key != "ABC-2" || ts[1].StatusCategory != model.StatusDone ||
		!ts[1].StatusSince.Equal(time.Date(2026, 10, 3, 12, 0, 0, 0, edt)) {
		t.Errorf("ts[1] = %+v", ts[1])
	}
}

// ticketEqual compares times with Equal, so zones need not match.
func ticketEqual(a, b model.Ticket) bool {
	return a.Key == b.Key && a.Title == b.Title && a.Status == b.Status &&
		a.StatusCategory == b.StatusCategory && a.URL == b.URL &&
		a.Updated.Equal(b.Updated) && a.StatusSince.Equal(b.StatusSince)
}

func TestMyTicketsFollowsPages(t *testing.T) {
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NextPageToken string `json:"nextPageToken"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		tokens = append(tokens, body.NextPageToken)
		if body.NextPageToken == "p2" {
			http.ServeFile(w, r, "testdata/search_page2.json")
			return
		}
		http.ServeFile(w, r, "testdata/search_page1.json")
	}))
	defer srv.Close()
	ts, _, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tokens, ",") != ",p2" {
		t.Errorf("page tokens sent = %q, want first page then p2", tokens)
	}
	if len(ts) != 2 || ts[0].Key != "ABC-1" || ts[1].Key != "ABC-3" {
		t.Fatalf("tickets = %+v", ts)
	}
	if !ts[1].StatusSince.IsZero() {
		t.Errorf("null statuscategorychangedate gave StatusSince %v, want zero", ts[1].StatusSince)
	}
}

func TestMyTicketsRepeatedPageTokenIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/search_page1.json") // always says "next is p2"
	}))
	defer srv.Close()
	_, _, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err = %v, want repeated-token error", err)
	}
}

func TestTicketsByKey(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JQL string `json:"jql"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotJQL = body.JQL
		http.ServeFile(w, r, "testdata/search.json")
	}))
	defer srv.Close()
	ts, _, err := jira.New(srv.URL, "me@example.com", "tok").TicketsByKey(context.Background(), []string{"ABC-1", "ABC-2"})
	if err != nil {
		t.Fatal(err)
	}
	if gotJQL != "key in (ABC-1,ABC-2)" {
		t.Errorf("jql = %q", gotJQL)
	}
	if len(ts) != 2 || ts[0].Key != "ABC-1" || ts[1].StatusCategory != model.StatusDone {
		t.Errorf("tickets = %+v", ts)
	}
}

func TestTicketsByKeyEmptyMakesNoRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.ServeFile(w, r, "testdata/search.json")
	}))
	defer srv.Close()
	ts, _, err := jira.New(srv.URL, "me@example.com", "tok").TicketsByKey(context.Background(), nil)
	if err != nil || len(ts) != 0 || calls != 0 {
		t.Errorf("tickets = %+v, err = %v, requests = %d; want none, nil, 0", ts, err, calls)
	}
}

func TestUnauthorizedIsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorMessages":["Client must be authenticated"]}`))
	}))
	defer srv.Close()
	_, _, err := jira.New(srv.URL, "me@example.com", "bad").MyTickets(context.Background(), []string{"ABC"})
	if !jira.IsAuth(err) {
		t.Fatalf("err = %v, want IsAuth", err)
	}
}

func TestSearchForbiddenIsNotAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, _, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	if err == nil || jira.IsAuth(err) {
		t.Fatalf("err = %v, want a non-auth error", err)
	}
}

func TestSearchRateLimitCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, _, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	var ae *jira.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests || ae.RetryAfter != 30*time.Second {
		t.Fatalf("err = %#v, want *APIError 429 with RetryAfter 30s", err)
	}
	if jira.IsAuth(err) {
		t.Error("429 reported as auth error")
	}
}

func TestRateLimitWithoutRetryAfterDefaultsToMinute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, _, err := jira.New(srv.URL, "me@example.com", "tok").TicketsByKey(context.Background(), []string{"ABC-1"})
	var ae *jira.APIError
	if !errors.As(err, &ae) || ae.RetryAfter != time.Minute {
		t.Fatalf("err = %#v, want RetryAfter 1m", err)
	}
}

func TestTransitions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/issue/ABC-1/transitions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"transitions":[
			{"id":"21","name":"Start review","to":{"name":"Code Review"}},
			{"id":"31","name":"Finish","to":{"name":"Done"}}]}`))
	}))
	defer srv.Close()
	got, err := jira.New(srv.URL, "me@example.com", "tok").Transitions(context.Background(), "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []jira.Transition{{ID: "21", Name: "Start review", ToName: "Code Review"}, {ID: "31", Name: "Finish", ToName: "Done"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("transitions = %+v, want %+v", got, want)
	}
}

func TestDoTransition(t *testing.T) {
	var method, path, id string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		var body struct {
			Transition struct {
				ID string `json:"id"`
			} `json:"transition"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id = body.Transition.ID
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := jira.New(srv.URL, "me@example.com", "tok").DoTransition(context.Background(), "ABC-1", "31"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/api/3/issue/ABC-1/transitions" || id != "31" {
		t.Errorf("got %s %s id=%q", method, path, id)
	}
}

func TestMalformedFieldBlanksOnlyThatField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/search_malformed.json")
	}))
	defer srv.Close()
	ts, warnings, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 || !strings.HasPrefix(warnings[0], "ABC-1 updated: ") ||
		!strings.HasPrefix(warnings[1], "ABC-2 statuscategorychangedate: ") {
		t.Errorf("warnings = %q", warnings)
	}
	if len(ts) != 2 {
		t.Fatalf("got %d tickets, want 2: %+v", len(ts), ts)
	}
	edt := time.FixedZone("", -4*3600)
	want := []model.Ticket{{
		Key:            "ABC-1",
		Title:          "Add widget export",
		Status:         "Code Review",
		StatusCategory: model.StatusInProgress,
		URL:            srv.URL + "/browse/ABC-1",
		StatusSince:    time.Date(2026, 10, 1, 8, 30, 0, 0, edt), // Updated is bad: zero
	}, {
		Key:            "ABC-2",
		Title:          "Fix gadget rounding",
		Status:         "Done",
		StatusCategory: model.StatusDone,
		URL:            srv.URL + "/browse/ABC-2",
		Updated:        time.Date(2026, 10, 4, 15, 0, 0, 0, edt), // StatusSince is bad: zero
	}}
	for i := range want {
		if !ticketEqual(ts[i], want[i]) {
			t.Errorf("ts[%d] = %+v, want %+v", i, ts[i], want[i])
		}
	}
}

// countingServer counts requests and answers with the two-ticket fixture.
func countingServer(t *testing.T, calls *int) *jira.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		http.ServeFile(w, r, "testdata/search.json")
	}))
	t.Cleanup(srv.Close)
	return jira.New(srv.URL, "me@example.com", "tok")
}

func TestMyTicketsNoProjectsIsError(t *testing.T) {
	calls := 0
	_, _, err := countingServer(t, &calls).MyTickets(context.Background(), nil)
	if err == nil || calls != 0 {
		t.Errorf("err = %v, requests = %d; want an error and no request", err, calls)
	}
}

func TestMyTicketsBadProjectKeyIsError(t *testing.T) {
	for _, bad := range []string{"abc", "A", "1AB", "AB) OR (1=1", "AB-1", ""} {
		calls := 0
		_, _, err := countingServer(t, &calls).MyTickets(context.Background(), []string{"ABC", bad})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", bad)) || calls != 0 {
			t.Errorf("project %q: err = %v, requests = %d; want an error naming it and no request", bad, err, calls)
		}
	}
}

func TestTicketsByKeyBadKeyIsError(t *testing.T) {
	for _, bad := range []string{"abc-1", "ABC", "ABC-", "ABC-1x", "ABC-1) OR (1=1", ""} {
		calls := 0
		_, _, err := countingServer(t, &calls).TicketsByKey(context.Background(), []string{"ABC-1", bad})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", bad)) || calls != 0 {
			t.Errorf("key %q: err = %v, requests = %d; want an error naming it and no request", bad, err, calls)
		}
	}
}

func TestTicketKeyIsUppercased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"abc-7","fields":{"summary":"x"}}],"isLast":true}`))
	}))
	defer srv.Close()
	ts, _, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	if err != nil || len(ts) != 1 || ts[0].Key != "ABC-7" || ts[0].URL != srv.URL+"/browse/ABC-7" {
		t.Fatalf("tickets = %+v, err = %v; want key ABC-7", ts, err)
	}
}

func TestSearchPageCapIsError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprintf(w, `{"issues":[],"nextPageToken":"p%d","isLast":false}`, calls)
	}))
	defer srv.Close()
	_, _, err := jira.New(srv.URL, "me@example.com", "tok").MyTickets(context.Background(), []string{"ABC"})
	if err == nil || !strings.Contains(err.Error(), "more than 50 pages") || calls != 50 {
		t.Fatalf("err = %v after %d requests; want page-cap error after 50", err, calls)
	}
}

func TestCancelledContext(t *testing.T) {
	calls := 0
	c := countingServer(t, &calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.MyTickets(ctx, []string{"ABC"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A success body is read to the end, so the keep-alive connection is reused.
func TestSuccessBodyIsDrainedForReuse(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"accountId":"acct-me"}` + strings.Repeat(" ", 64<<10)))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			mu.Lock()
			conns++
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()
	c := jira.New(srv.URL, "me@example.com", "tok")
	for range 3 {
		if _, err := c.Myself(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 1 {
		t.Errorf("opened %d connections for 3 requests, want 1", conns)
	}
}
