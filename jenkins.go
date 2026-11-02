package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Jenkins reads the Jenkins JSON API, server side, with Basic auth
// (user + API token). Every request is recorded as a Call so the page can
// show what the line asked Jenkins, without the token.
type Jenkins struct {
	cfg  Config
	http *http.Client
}

// Call is one HTTP request made to Jenkins, as shown on the page.
type Call struct {
	Path   string `json:"path"`
	Status int    `json:"status"` // 0 = no answer
	Millis int64  `json:"ms"`
	Error  string `json:"error,omitempty"`

	version string // X-Jenkins response header
}

var errNotFound = errors.New("not found")

type httpError struct{ status int }

func (e httpError) Error() string { return fmt.Sprintf("HTTP %d", e.status) }

func (j *Jenkins) get(ctx context.Context, path string, into any) (Call, error) {
	call := Call{Path: path}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.cfg.JenkinsURL+path, nil)
	if err != nil {
		return call, err
	}
	req.Header.Set("Accept", "application/json")
	if j.cfg.hasToken() {
		req.SetBasicAuth(j.cfg.User, j.cfg.Token)
	}
	resp, err := j.http.Do(req)
	call.Millis = time.Since(start).Milliseconds()
	if err != nil {
		call.Error = "no answer"
		return call, err
	}
	defer resp.Body.Close()
	call.Status = resp.StatusCode
	call.version = resp.Header.Get("X-Jenkins")
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return call, errNotFound
	case resp.StatusCode != http.StatusOK:
		return call, httpError{resp.StatusCode}
	case into == nil:
		return call, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into); err != nil {
		call.Error = "bad JSON"
		return call, err
	}
	return call, nil
}

// ---- Raw Jenkins shapes (only the fields we use) ----

type jobJSON struct {
	Builds []struct {
		Number    int    `json:"number"`
		Result    string `json:"result"` // SUCCESS, FAILURE, ABORTED, UNSTABLE, "" while building
		Building  bool   `json:"building"`
		Timestamp int64  `json:"timestamp"`
		Duration  int64  `json:"duration"`
		Actions   []struct {
			Class  string `json:"_class"`
			Causes []struct {
				Class string `json:"_class"`
			} `json:"causes"`
			LastBuiltRevision *struct {
				SHA1 string `json:"SHA1"`
			} `json:"lastBuiltRevision"`
		} `json:"actions"`
	} `json:"builds"`
}

// wfapi/runs: Pipeline Stage View plugin, all recent runs with their stages.
type wfRun struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Stages []struct {
		Name           string `json:"name"`
		Status         string `json:"status"` // SUCCESS, FAILED, IN_PROGRESS, NOT_EXECUTED, ABORTED, UNSTABLE…
		DurationMillis int64  `json:"durationMillis"`
	} `json:"stages"`
}

// stages/tree: Pipeline Graph View plugin, one run.
type graphTree struct {
	Data struct {
		Stages []struct {
			Name                string `json:"name"`
			State               string `json:"state"` // success, failure, running, skipped, not_built…
			TotalDurationMillis int64  `json:"totalDurationMillis"`
		} `json:"stages"`
	} `json:"data"`
}

type computersJSON struct {
	Computer []struct {
		Class       string `json:"_class"`
		DisplayName string `json:"displayName"`
		Offline     bool   `json:"offline"`
		Idle        bool   `json:"idle"`
	} `json:"computer"`
}

type labelJSON struct {
	Clouds []struct{} `json:"clouds"`
	Nodes  []struct {
		NodeName string `json:"nodeName"`
	} `json:"nodes"`
}

type credentialsJSON struct {
	Credentials []struct {
		ID string `json:"id"`
	} `json:"credentials"`
}

// ---- Snapshot: one consistent reading of the line ----

// RunnerLabel is the label of the Clever runner template the Jenkinsfile asks for.
const RunnerLabel = "clever-go"

type Stage struct {
	Name     string
	Status   string // done, running, failed, skipped, stopped, waiting
	Duration time.Duration
}

type Build struct {
	Number   int
	Result   string
	Building bool
	Started  time.Time
	Duration time.Duration
	Commit   string
	Cause    string
	Stages   []Stage
}

type Runner struct {
	Name   string `json:"name"`
	Online bool   `json:"online"`
	Busy   bool   `json:"busy"`
}

type Snapshot struct {
	State       string // unconfigured, awaiting-token, unreachable, unauthorized, no-job, connected
	Detail      string
	Version     string // X-Jenkins header, when known
	Builds      []Build
	Runners     []Runner
	RunnerCloud *bool // a cloud (Clever runners) can provision the label
	Credentials *bool // clever-token and clever-secret exist
	Calls       []Call
	FetchedAt   time.Time
}

// Line caches the latest snapshot so many viewers cost one reading every few seconds.
type Line struct {
	jenkins *Jenkins
	ttl     time.Duration
	mu      sync.Mutex
	last    *Snapshot
}

func newLine(cfg Config) *Line {
	return &Line{
		jenkins: &Jenkins{cfg: cfg, http: &http.Client{Timeout: 5 * time.Second}},
		ttl:     4 * time.Second,
	}
}

func (l *Line) Snapshot(ctx context.Context) Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	ttl := l.ttl
	if l.last != nil && l.last.State != "connected" {
		ttl = 15 * time.Second // nothing moves on a line that is not connected
	}
	if l.last != nil && time.Since(l.last.FetchedAt) < ttl {
		return *l.last
	}
	s := l.jenkins.read(ctx)
	l.last = &s
	return s
}

func (j *Jenkins) read(ctx context.Context) Snapshot {
	s := Snapshot{FetchedAt: time.Now()}
	if j.cfg.JenkinsURL == "" {
		s.State, s.Detail = "unconfigured", "No Jenkins add-on linked (JENKINS_TUNNEL or JENKINS_URL is not set)."
		return s
	}
	if !j.cfg.hasToken() {
		// Without a token we can only check that Jenkins answers (Clever SSO in front).
		call, err := j.get(ctx, "/login", nil)
		s.Calls = append(s.Calls, call)
		s.Version = call.version
		if call.Status == 0 || call.Status >= 500 || errors.Is(err, errNotFound) {
			s.State, s.Detail = "unreachable", "Jenkins does not answer yet."
			return s
		}
		s.State, s.Detail = "awaiting-token", "Jenkins answers. JENKINS_USER and JENKINS_API_TOKEN are not set on the product app."
		return s
	}

	jobPath := "/job/" + url.PathEscape(j.cfg.Job)
	var mu sync.Mutex
	record := func(c Call) {
		mu.Lock()
		defer mu.Unlock()
		s.Calls = append(s.Calls, c)
		if s.Version == "" {
			s.Version = c.version
		}
	}

	var job jobJSON
	call, err := j.get(ctx, jobPath+"/api/json?tree=builds[number,result,building,timestamp,duration,"+
		"actions[causes[_class],lastBuiltRevision[SHA1]]]{0,8}", &job)
	record(call)

	// The checks below run alongside: runners on the floor, runner template, credentials.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		var comp computersJSON
		c, err := j.get(ctx, "/computer/api/json?tree=computer[displayName,offline,idle]", &comp)
		record(c)
		if err == nil {
			for _, k := range comp.Computer {
				if strings.Contains(k.Class, "MasterComputer") || strings.Contains(k.Class, "BuiltIn") {
					continue // the controller itself is not a runner
				}
				s.Runners = append(s.Runners, Runner{Name: k.DisplayName, Online: !k.Offline, Busy: !k.Idle})
			}
		}
	}()
	go func() {
		defer wg.Done()
		var lbl labelJSON
		c, err := j.get(ctx, "/label/"+RunnerLabel+"/api/json?tree=clouds[name],nodes[nodeName]", &lbl)
		record(c)
		if err == nil {
			ok := len(lbl.Clouds) > 0 || len(lbl.Nodes) > 0
			s.RunnerCloud = &ok
		}
	}()
	go func() {
		defer wg.Done()
		var cr credentialsJSON
		c, err := j.get(ctx, "/credentials/store/system/domain/_/api/json?tree=credentials[id]", &cr)
		record(c)
		if err == nil {
			have := map[string]bool{}
			for _, k := range cr.Credentials {
				have[k.ID] = true
			}
			ok := have["clever-token"] && have["clever-secret"]
			s.Credentials = &ok
		}
	}()

	switch {
	case call.Status == 0 || call.Status >= 500:
		wg.Wait()
		s.State, s.Detail = "unreachable", "Jenkins does not answer."
		return s
	case call.Status == http.StatusUnauthorized || call.Status == http.StatusForbidden:
		wg.Wait()
		s.State, s.Detail = "unauthorized", "Jenkins refused the API token (check JENKINS_USER and JENKINS_API_TOKEN)."
		return s
	case errors.Is(err, errNotFound):
		wg.Wait()
		s.State, s.Detail = "no-job", fmt.Sprintf("Jenkins has no job named %q yet.", j.cfg.Job)
		return s
	case err != nil:
		wg.Wait()
		s.State, s.Detail = "unreachable", "Jenkins answered with something unexpected."
		return s
	}

	stages := j.readStages(ctx, jobPath, &job, record)
	wg.Wait()

	for _, b := range job.Builds {
		build := Build{
			Number:   b.Number,
			Result:   b.Result,
			Building: b.Building,
			Started:  time.UnixMilli(b.Timestamp),
			Duration: time.Duration(b.Duration) * time.Millisecond,
			Stages:   stages[b.Number],
		}
		if b.Building {
			build.Duration = time.Since(build.Started).Round(time.Second)
		}
		for _, a := range b.Actions {
			if a.LastBuiltRevision != nil && build.Commit == "" {
				build.Commit = a.LastBuiltRevision.SHA1
			}
			for _, c := range a.Causes {
				if build.Cause == "" {
					build.Cause = causeLabel(c.Class)
				}
			}
		}
		s.Builds = append(s.Builds, build)
	}
	s.State = "connected"
	return s
}

// readStages prefers the Stage View API (one call for every run) and falls back
// to the Graph View API (one call per run) when that plugin is not installed.
func (j *Jenkins) readStages(ctx context.Context, jobPath string, job *jobJSON, record func(Call)) map[int][]Stage {
	out := map[int][]Stage{}
	var runs []wfRun
	call, err := j.get(ctx, jobPath+"/wfapi/runs", &runs)
	record(call)
	if err == nil {
		for _, r := range runs {
			n, _ := strconv.Atoi(r.ID)
			for _, st := range r.Stages {
				out[n] = append(out[n], Stage{Name: st.Name, Status: wfStatus(st.Status), Duration: ms(st.DurationMillis)})
			}
		}
		return out
	}
	// Graph View: /stages/tree on current versions, /pipeline-graph/tree on older ones.
	graph := "/stages/tree"
	for i, b := range job.Builds {
		if i == 4 {
			break // keep the fallback cheap
		}
		var tree graphTree
		call, err := j.get(ctx, fmt.Sprintf("%s/%d%s", jobPath, b.Number, graph), &tree)
		record(call)
		if errors.Is(err, errNotFound) && i == 0 && graph == "/stages/tree" {
			graph = "/pipeline-graph/tree"
			call, err = j.get(ctx, fmt.Sprintf("%s/%d%s", jobPath, b.Number, graph), &tree)
			record(call)
		}
		if errors.Is(err, errNotFound) && i == 0 {
			break // no Graph View plugin either: builds without stations
		}
		for _, st := range tree.Data.Stages {
			out[b.Number] = append(out[b.Number], Stage{Name: st.Name, Status: graphStatus(st.State), Duration: ms(st.TotalDurationMillis)})
		}
	}
	return out
}

func ms(v int64) time.Duration { return time.Duration(v) * time.Millisecond }

func wfStatus(s string) string {
	switch s {
	case "SUCCESS", "UNSTABLE":
		return "done"
	case "IN_PROGRESS", "PAUSED_PENDING_INPUT":
		return "running"
	case "FAILED":
		return "failed"
	case "ABORTED":
		return "stopped"
	case "NOT_EXECUTED":
		return "skipped"
	}
	return "waiting"
}

func graphStatus(s string) string {
	switch strings.ToLower(s) {
	case "success", "unstable":
		return "done"
	case "running", "paused":
		return "running"
	case "failure":
		return "failed"
	case "aborted":
		return "stopped"
	case "skipped", "not_built":
		return "skipped"
	}
	return "waiting"
}

// causeLabel names a trigger without exposing who clicked.
func causeLabel(class string) string {
	switch {
	case strings.HasSuffix(class, "UserIdCause"):
		return "started by hand"
	case strings.Contains(class, "SCMTrigger"), strings.Contains(class, "Push"), strings.Contains(class, "Branch"):
		return "new commit"
	case strings.Contains(class, "TimerTrigger"):
		return "on schedule"
	case strings.Contains(class, "Replay"):
		return "replayed"
	case strings.Contains(class, "Upstream"):
		return "upstream job"
	}
	return "triggered"
}
