package main

import (
	"fmt"
	"strings"
	"time"
)

// The four stations of the line, in order, matched to the Jenkinsfile stages.
var stations = []struct{ Key, Stage, Machine string }{
	{"checkout", "Checkout", "Goods in"},
	{"test", "Test", "Quality control"},
	{"build", "Build", "Press"},
	{"deploy", "Deploy", "Dock to Clever Cloud"},
}

// FloorView is what the browser receives: ready to draw, no secret inside.
type FloorView struct {
	State       string       `json:"state"`
	Andon       string       `json:"andon"` // green, amber, red, off
	Headline    string       `json:"headline"`
	Detail      string       `json:"detail"`
	NextStep    string       `json:"nextStep"` // the setup step still to do, empty once connected
	JenkinsURL  string       `json:"jenkinsUrl"`
	Version     string       `json:"version"`
	Job         string       `json:"job"`
	Current     *BuildView   `json:"current"`
	Builds      []BuildView  `json:"builds"`
	Runners     []Runner     `json:"runners"`
	Calls       []Call       `json:"calls"`
	Checklist   []CheckItem  `json:"checklist"`
	Unit        UnitView     `json:"unit"`
	Stations    []StationDef `json:"stations"`
	FetchedAt   time.Time    `json:"fetchedAt"`
	RunnerLabel string       `json:"runnerLabel"`
}

type StationDef struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Machine string `json:"machine"`
}

type BuildView struct {
	Number   int           `json:"number"`
	Outcome  string        `json:"outcome"` // shipped, passed, rejected, running, stopped
	Stamp    string        `json:"stamp"`
	Started  time.Time     `json:"started"`
	Duration string        `json:"duration"`
	Commit   string        `json:"commit"`
	Cause    string        `json:"cause"`
	Position int           `json:"position"` // station index where the crate is; 4 = through the dock
	Stations []StationView `json:"stations"`
}

type StationView struct {
	Key      string `json:"key"`
	Status   string `json:"status"` // done, running, failed, skipped, stopped, waiting
	Duration string `json:"duration"`
}

type CheckItem struct {
	Title  string `json:"title"`
	Hint   string `json:"hint"`
	Status string `json:"status"` // done, todo, unknown
	Owner  bool   `json:"owner"`  // needs the owner's Clever account
}

type UnitView struct {
	Commit    string `json:"commit"`
	Build     int    `json:"build"` // Jenkins build that carries this commit, 0 = none
	Verdict   string `json:"verdict"`
	ByTheLine bool   `json:"byTheLine"`
}

func buildFloor(cfg Config, p Platform, s Snapshot) FloorView {
	v := FloorView{
		State:       s.State,
		Detail:      s.Detail,
		JenkinsURL:  cfg.JenkinsURL,
		Version:     s.Version,
		Job:         cfg.Job,
		Runners:     s.Runners,
		Calls:       s.Calls,
		FetchedAt:   s.FetchedAt,
		RunnerLabel: RunnerLabel,
		Builds:      []BuildView{},
	}
	for _, st := range stations {
		v.Stations = append(v.Stations, StationDef{st.Key, st.Stage, st.Machine})
	}
	if v.Runners == nil {
		v.Runners = []Runner{}
	}
	if v.Calls == nil {
		v.Calls = []Call{}
	}
	for _, b := range s.Builds {
		v.Builds = append(v.Builds, buildView(b))
	}
	if len(v.Builds) > 0 {
		v.Current = &v.Builds[0]
	}
	v.Andon, v.Headline = andon(s, v.Current)
	v.NextStep = nextStep(s.State)
	v.Checklist = checklist(cfg, s)
	v.Unit = unit(p, s)
	return v
}

func buildView(b Build) BuildView {
	bv := BuildView{
		Number:   b.Number,
		Started:  b.Started,
		Duration: humanDuration(b.Duration),
		Commit:   short(b.Commit, 7),
		Cause:    b.Cause,
	}
	byName := map[string]Stage{}
	for _, st := range b.Stages {
		byName[strings.ToLower(st.Name)] = st
	}
	for _, def := range stations {
		sv := StationView{Key: def.Key, Status: "waiting"}
		if st, ok := byName[strings.ToLower(def.Stage)]; ok {
			sv.Status = st.Status
			if st.Status != "skipped" && st.Status != "waiting" {
				sv.Duration = humanDuration(st.Duration)
			}
		}
		bv.Stations = append(bv.Stations, sv)
	}
	// The Stage View API marks every stage after a failure as failed too:
	// the crate never reached them, so they are skipped.
	if i := firstStation(bv.Stations, "failed"); i >= 0 {
		for j := i + 1; j < len(bv.Stations); j++ {
			if bv.Stations[j].Status == "failed" {
				bv.Stations[j] = StationView{Key: bv.Stations[j].Key, Status: "skipped"}
			}
		}
	}
	bv.Position = firstStation(bv.Stations, "running", "failed", "stopped")
	if b.Building && bv.Position == -1 {
		// Between two stages: the crate is on its way to the next station.
		if i := firstStation(bv.Stations, "waiting"); i >= 0 {
			bv.Stations[i].Status = "running"
			bv.Position = i
		}
	}

	deploy := bv.Stations[3].Status
	switch {
	case b.Building:
		bv.Outcome, bv.Stamp = "running", "On the line"
		if bv.Position == -1 {
			bv.Position = 0
		}
	case b.Result == "SUCCESS" && deploy == "done":
		bv.Outcome, bv.Stamp, bv.Position = "shipped", "Shipped", 4
	case b.Result == "SUCCESS" || b.Result == "UNSTABLE":
		bv.Outcome, bv.Stamp, bv.Position = "passed", "Passed, kept in stock", 3
	case b.Result == "ABORTED":
		bv.Outcome, bv.Stamp = "stopped", "Line stopped"
	default:
		bv.Outcome, bv.Stamp = "rejected", "Rejected"
	}
	if bv.Position == -1 {
		bv.Position = 0
	}
	if bv.Outcome == "rejected" {
		bv.Stations[bv.Position].Status = "failed"
		bv.Stamp = "Rejected at " + strings.ToLower(stations[bv.Position].Stage)
	}
	return bv
}

func firstStation(list []StationView, statuses ...string) int {
	for i, sv := range list {
		for _, want := range statuses {
			if sv.Status == want {
				return i
			}
		}
	}
	return -1
}

// sameCommit compares a full SHA with a full or abbreviated one.
func sameCommit(a, b string) bool {
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

func andon(s Snapshot, cur *BuildView) (string, string) {
	switch s.State {
	case "unconfigured":
		return "off", "No Jenkins linked to this line."
	case "unreachable":
		return "off", "Jenkins is not answering."
	case "awaiting-token":
		return "amber", "Waiting for connection: Jenkins is up, the line has no API token yet."
	case "unauthorized":
		return "red", "Jenkins refused the line's API token."
	case "no-job":
		return "amber", "Jenkins is connected. The assembly-line job is not created yet."
	}
	if cur == nil {
		return "amber", "Connected. No build has run yet."
	}
	switch cur.Outcome {
	case "running":
		return "amber", fmt.Sprintf("Build #%d is at %s.", cur.Number, strings.ToLower(stations[min(cur.Position, 3)].Machine))
	case "shipped":
		return "green", fmt.Sprintf("Build #%d shipped to Clever Cloud.", cur.Number)
	case "passed":
		return "green", fmt.Sprintf("Build #%d passed every check, kept in stock.", cur.Number)
	case "stopped":
		return "amber", fmt.Sprintf("Build #%d was stopped.", cur.Number)
	}
	return "red", fmt.Sprintf("Build #%d was rejected at %s.", cur.Number, strings.ToLower(stations[cur.Position].Machine))
}

// nextStep names the setup step that is missing while the line is not connected.
// The page shows it under the andon board, /health/deep returns it with its 503.
func nextStep(state string) string {
	switch state {
	case "unconfigured":
		return "Create the Jenkins add-on and link it to this app (it injects JENKINS_TUNNEL), then restart."
	case "unreachable":
		return "Wait for the Jenkins add-on to finish starting, or check its address."
	case "awaiting-token":
		return "Do the README section \"Connect the line\": open Jenkins from the Clever Cloud Console, create an API token (your user › Security › API Token), set JENKINS_USER and JENKINS_API_TOKEN on this app, then restart."
	case "unauthorized":
		return "Create a new API token in Jenkins, set JENKINS_USER and JENKINS_API_TOKEN again on this app, then restart."
	}
	return ""
}

func checklist(cfg Config, s Snapshot) []CheckItem {
	connected := s.State == "connected" || s.State == "no-job"
	status := func(known bool, ok bool) string {
		switch {
		case !known:
			return "unknown"
		case ok:
			return "done"
		}
		return "todo"
	}
	ptr := func(b *bool) (bool, bool) {
		if b == nil {
			return false, false
		}
		return true, *b
	}
	cloudKnown, cloudOK := ptr(s.RunnerCloud)
	credKnown, credOK := ptr(s.Credentials)
	reachable := s.State != "unconfigured" && s.State != "unreachable"
	tokenStatus := "unknown"
	switch {
	case !cfg.hasToken() || s.State == "unauthorized":
		tokenStatus = "todo"
	case connected:
		tokenStatus = "done"
	}

	return []CheckItem{
		{"Jenkins add-on provisioned", "Created with clever addon create jenkins, linked to the product app.", status(true, reachable), false},
		{"Sign in once with your Clever Cloud account", "Open Jenkins: Clever SSO signs you in. Accept every permission the first time.", status(connected, connected), true},
		{"Give the factory floor a Jenkins API token", "Your user › Security › API Token, then clever env set JENKINS_USER and JENKINS_API_TOKEN.", tokenStatus, true},
		{"Add the Clever runners", "Manage Jenkins › Clouds › New cloud › Clever Cloud, runner template labelled " + RunnerLabel + ", image jenkins/inbound-agent.", status(cloudKnown, cloudOK), true},
		{"Store the Clever deploy credentials", "Two Secret text credentials: clever-token and clever-secret.", status(credKnown, credOK), true},
		{"Create the " + cfg.Job + " pipeline job", "New Item › Pipeline › Pipeline script from SCM, this repository, script path Jenkinsfile.", status(connected, s.State == "connected"), true},
		{"Run the first build", "Build Now. The crate rides the belt on this page.", status(s.State == "connected", len(s.Builds) > 0), true},
	}
}

func unit(p Platform, s Snapshot) UnitView {
	u := UnitView{Commit: p.Commit}
	if p.CommitFull == "" {
		u.Verdict = "Running locally: no deployment commit."
		return u
	}
	for _, b := range s.Builds {
		if sameCommit(b.Commit, p.CommitFull) {
			u.Build = b.Number
			u.ByTheLine = true
			u.Verdict = fmt.Sprintf("Assembled on the line by build #%d.", b.Number)
			return u
		}
	}
	if s.State != "connected" {
		u.Verdict = "Not built by the line yet: deployed with clever deploy."
		return u
	}
	u.Verdict = "No recent Jenkins build carries this commit: deployed by hand with clever deploy."
	return u
}

func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Second:
		return "<1 s"
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		sec := int(d.Seconds()) - m*60
		if sec == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %02d s", m, sec)
	}
	return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
}
