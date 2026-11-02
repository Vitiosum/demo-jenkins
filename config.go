package main

import (
	"net"
	"os"
	"strings"
)

// Config is everything the factory floor needs to read Jenkins.
// The token stays on the server: it is never sent to the browser.
type Config struct {
	JenkinsURL string // public Jenkins URL, https://<host>
	Job        string // pipeline job that builds this repo
	User       string // Jenkins user id that owns the API token
	Token      string // Jenkins API token (Jenkins › user › Security › API Token)
}

func loadConfig() Config {
	return Config{
		JenkinsURL: jenkinsURL(os.Getenv("JENKINS_URL"), os.Getenv("JENKINS_TUNNEL")),
		Job:        envOr("JENKINS_JOB", "assembly-line"),
		User:       strings.TrimSpace(os.Getenv("JENKINS_USER")),
		Token:      strings.TrimSpace(os.Getenv("JENKINS_API_TOKEN")),
	}
}

// jenkinsURL prefers an explicit JENKINS_URL. Otherwise it reads the add-on's
// own JENKINS_TUNNEL ("<host>:<agent port>") and keeps the host: the Jenkins
// web interface is served over HTTPS on that same host.
func jenkinsURL(explicit, tunnel string) string {
	if u := strings.TrimRight(strings.TrimSpace(explicit), "/"); u != "" {
		return u
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(tunnel))
	if err != nil || host == "" {
		return ""
	}
	return "https://" + host
}

func (c Config) hasToken() bool { return c.User != "" && c.Token != "" }

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// Platform holds the Clever Cloud variables shown in the "Running on Clever
// Cloud" panel. Only these are ever displayed.
type Platform struct {
	AppName, AppID, Instance, InstanceType, PrettyName, Commit, CommitFull, Deployment string
}

func loadPlatform() Platform {
	commit := envOr("CC_COMMIT_ID", os.Getenv("COMMIT_ID"))
	return Platform{
		AppName:      os.Getenv("CC_APP_NAME"),
		AppID:        os.Getenv("APP_ID"),
		Instance:     os.Getenv("INSTANCE_NUMBER"),
		InstanceType: os.Getenv("INSTANCE_TYPE"),
		PrettyName:   os.Getenv("CC_PRETTY_INSTANCE_NAME"),
		Commit:       short(commit, 7),
		CommitFull:   commit,
		Deployment:   envOr("CC_DEPLOYMENT_ID", os.Getenv("DEPLOYMENT_ID")),
	}
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
