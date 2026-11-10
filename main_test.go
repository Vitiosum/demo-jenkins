package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The fixtures in testdata/ are real answers recorded from Jenkins 2.580.2
// running this repository's Jenkinsfile (builds 1 to 5).
const testUser, testToken = "factory", "test-token-0123456789"

// fakeJenkins replays the fixtures and enforces Basic auth like Jenkins does.
func fakeJenkins(t *testing.T, suffix string, withStageView bool) *httptest.Server {
	t.Helper()
	routes := map[string]string{
		"/job/assembly-line/api/json":                 "job" + suffix + ".json",
		"/job/assembly-line/wfapi/runs":               "wfapi-runs" + suffix + ".json",
		"/job/assembly-line/4/stages/tree":            "stages-tree-4.json",
		"/job/assembly-line/2/stages/tree":            "stages-tree-2.json",
		"/computer/api/json":                          "computer.json",
		"/label/clever-go/api/json":                   "label.json",
		"/credentials/store/system/domain/_/api/json": "credentials.json",
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Jenkins", "2.580.2")
		if r.URL.Path == "/login" {
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != testUser || p != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		file, ok := routes[r.URL.Path]
		if !ok || (!withStageView && strings.Contains(r.URL.Path, "wfapi")) {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func floorOf(t *testing.T, h http.Handler) FloorView {
	t.Helper()
	rec := get(t, h, "/api/floor")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/floor = %d", rec.Code)
	}
	var v FloorView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func connected(t *testing.T, suffix string, stageView bool) (*httptest.Server, http.Handler) {
	jk := fakeJenkins(t, suffix, stageView)
	t.Cleanup(jk.Close)
	cfg := Config{JenkinsURL: jk.URL, Job: "assembly-line", User: testUser, Token: testToken}
	return jk, newServer(cfg, Platform{}).routes()
}

func TestHealthNeverTouchesJenkins(t *testing.T) {
	h := newServer(Config{JenkinsURL: "http://127.0.0.1:1", Job: "assembly-line"}, Platform{}).routes()
	if rec := get(t, h, "/health"); rec.Code != http.StatusOK {
		t.Fatalf("/health = %d", rec.Code)
	}
}

func TestNoJenkinsLinked(t *testing.T) {
	h := newServer(Config{Job: "assembly-line"}, Platform{}).routes()
	if rec := get(t, h, "/health/deep"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/health/deep = %d", rec.Code)
	}
	if v := floorOf(t, h); v.State != "unconfigured" || v.Andon != "off" || len(v.Calls) != 0 {
		t.Fatalf("state %s, andon %s, %d calls", v.State, v.Andon, len(v.Calls))
	}
}

func TestDeepHealthWaitsForToken(t *testing.T) {
	jk := fakeJenkins(t, "", true)
	defer jk.Close()
	h := newServer(Config{JenkinsURL: jk.URL, Job: "assembly-line"}, Platform{}).routes()
	rec := get(t, h, "/health/deep")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "awaiting-token") || !strings.Contains(rec.Body.String(), "Connect the line") {
		t.Fatalf("/health/deep = %d %s", rec.Code, rec.Body)
	}
	v := floorOf(t, h)
	if v.Andon != "amber" || v.Checklist[0].Status != "done" || v.Checklist[2].Status != "todo" {
		t.Fatalf("unexpected waiting floor: %+v", v.Checklist)
	}
}

func TestDeepHealthConnected(t *testing.T) {
	_, h := connected(t, "", true)
	rec := get(t, h, "/health/deep")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"builds":4`) {
		t.Fatalf("/health/deep = %d %s", rec.Code, rec.Body)
	}
}

func TestWrongTokenIsReported(t *testing.T) {
	jk := fakeJenkins(t, "", true)
	defer jk.Close()
	h := newServer(Config{JenkinsURL: jk.URL, Job: "assembly-line", User: testUser, Token: "wrong"}, Platform{}).routes()
	if v := floorOf(t, h); v.State != "unauthorized" || v.Andon != "red" {
		t.Fatalf("state = %s, andon = %s", v.State, v.Andon)
	}
}

func TestShiftLogFromStageView(t *testing.T) {
	_, h := connected(t, "", true)
	v := floorOf(t, h)
	if v.State != "connected" || len(v.Builds) != 4 {
		t.Fatalf("state %s, %d builds", v.State, len(v.Builds))
	}
	want := map[int]struct {
		outcome  string
		position int
		stations string
	}{
		4: {"passed", 3, "done done done skipped"},
		3: {"rejected", 1, "done failed skipped skipped"},
		2: {"rejected", 1, "done failed skipped skipped"},
		1: {"passed", 3, "done done done skipped"},
	}
	for _, b := range v.Builds {
		w := want[b.Number]
		if b.Outcome != w.outcome || b.Position != w.position || stationList(b) != w.stations {
			t.Errorf("build %d: %s at %d [%s], want %s at %d [%s]", b.Number, b.Outcome, b.Position, stationList(b), w.outcome, w.position, w.stations)
		}
	}
	if v.Builds[0].Commit == "" || v.Builds[0].Cause != "started by hand" || v.Version != "2.580.2" {
		t.Errorf("commit %q, cause %q, version %q", v.Builds[0].Commit, v.Builds[0].Cause, v.Version)
	}
}

func TestRunningBuild(t *testing.T) {
	_, h := connected(t, "-running", true)
	v := floorOf(t, h)
	cur := v.Current
	if cur == nil || cur.Number != 5 || cur.Outcome != "running" || cur.Position != 1 || v.Andon != "amber" {
		t.Fatalf("current = %+v, andon %s", cur, v.Andon)
	}
	if got := stationList(*cur); got != "done running waiting waiting" {
		t.Fatalf("stations = %s", got)
	}
}

func TestGraphViewFallback(t *testing.T) {
	_, h := connected(t, "", false) // no Stage View plugin: per-build /stages/tree
	v := floorOf(t, h)
	byNumber := map[int]BuildView{}
	for _, b := range v.Builds {
		byNumber[b.Number] = b
	}
	if got := stationList(byNumber[2]); got != "done failed skipped skipped" {
		t.Errorf("build 2 = %s", got)
	}
	if got := stationList(byNumber[4]); got != "done done done skipped" {
		t.Errorf("build 4 = %s", got)
	}
}

func TestTokenNeverReachesTheBrowser(t *testing.T) {
	_, h := connected(t, "", true)
	for _, path := range []string{"/", "/api/floor", "/health/deep"} {
		if body := get(t, h, path).Body.String(); strings.Contains(body, testToken) {
			t.Fatalf("%s leaks the Jenkins token", path)
		}
	}
}

func TestPageRendersWithCSP(t *testing.T) {
	_, h := connected(t, "", true)
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The Assembly Line") {
		t.Fatalf("/ = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("missing CSP")
	}
	if !strings.Contains(rec.Body.String(), "skipDefaultCheckout()") {
		t.Fatal("the Jenkinsfile blueprint is not on the page")
	}
}

func TestUnitMatchesTheBuildThatShippedIt(t *testing.T) {
	jk := fakeJenkins(t, "", true)
	defer jk.Close()
	var job jobJSON
	b, _ := os.ReadFile("testdata/job.json")
	_ = json.Unmarshal(b, &job)
	sha := "" // the commit build #4 checked out
	for _, a := range job.Builds[0].Actions {
		if a.LastBuiltRevision != nil {
			sha = a.LastBuiltRevision.SHA1
		}
	}
	cfg := Config{JenkinsURL: jk.URL, Job: "assembly-line", User: testUser, Token: testToken}
	h := newServer(cfg, Platform{Commit: short(sha, 7), CommitFull: sha}).routes()
	if u := floorOf(t, h).Unit; !u.ByTheLine || u.Build != 4 {
		t.Fatalf("unit = %+v", u)
	}
}

func TestJenkinsURLFromTheAddonTunnel(t *testing.T) {
	if got := jenkinsURL("", "abc-jenkins.services.clever-cloud.com:4242"); got != "https://abc-jenkins.services.clever-cloud.com" {
		t.Fatalf("got %q", got)
	}
	if got := jenkinsURL("https://ci.example.com/", "x:1"); got != "https://ci.example.com" {
		t.Fatalf("explicit URL ignored: %q", got)
	}
	if got := jenkinsURL("", ""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func stationList(b BuildView) string {
	parts := make([]string, len(b.Stations))
	for i, s := range b.Stations {
		parts[i] = s.Status
	}
	return strings.Join(parts, " ")
}
