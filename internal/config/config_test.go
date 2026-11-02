package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"GITHUB_TOKEN": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q", c.ListenAddr)
	}
	if c.RefreshInterval != 15*time.Minute {
		t.Errorf("RefreshInterval = %s", c.RefreshInterval)
	}
	if !c.IncludePrivate || !c.IncludeArchived || !c.IncludeForks {
		t.Errorf("unexpected bool defaults: %+v", c)
	}
	if c.GraphQLURL != defaultGraphQLURL {
		t.Errorf("GraphQLURL = %q", c.GraphQLURL)
	}
}

func TestExtraRepos(t *testing.T) {
	c, err := FromEnv(env(map[string]string{
		"GITHUB_TOKEN":      "x",
		"PRHUD_EXTRA_REPOS": "golang/go, someorg/*\n kubernetes/kubernetes",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"golang/go", "kubernetes/kubernetes"}; !slices.Equal(c.ExtraRepos, want) {
		t.Errorf("ExtraRepos = %v, want %v", c.ExtraRepos, want)
	}
	if want := []string{"someorg"}; !slices.Equal(c.ExtraOwners, want) {
		t.Errorf("ExtraOwners = %v, want %v", c.ExtraOwners, want)
	}
}

func TestErrorsAreCollected(t *testing.T) {
	_, err := FromEnv(env(map[string]string{
		"PRHUD_EXTRA_REPOS":      "nope,a/b/c",
		"PRHUD_REFRESH_INTERVAL": "5s",
		"PRHUD_INCLUDE_FORKS":    "maybe",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"GITHUB_TOKEN", `"nope"`, `"a/b/c"`, "PRHUD_REFRESH_INTERVAL", "PRHUD_INCLUDE_FORKS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
