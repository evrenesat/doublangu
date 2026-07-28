package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"runtime/debug"
)

func TestResolveSourceRevision_Precedence(t *testing.T) {
	const explicit = "deadbeefcafebabe"
	const candidateRevision = "abcdef1234567890abcdef1234567890abcdef12"
	candidate := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: candidateRevision}}}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gitOutput, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("read helper git revision: %v", err)
	}
	gitRevision := strings.TrimSpace(string(gitOutput))

	tests := []struct {
		name      string
		explicit  string
		candidate *debug.BuildInfo
		working   string
		want      string
	}{
		{"explicit overrides candidate and git", explicit, candidate, workingDir, explicit},
		{"candidate VCS overrides helper git", "", candidate, workingDir, candidateRevision},
		{"helper git fallback", "", &debug.BuildInfo{}, workingDir, gitRevision},
		{"unknown final fallback", "", &debug.BuildInfo{}, "", "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveSourceRevision(test.explicit, test.candidate, test.working)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("source revision = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildPlugin_InvalidExplicitRevisionDoesNotBuild(t *testing.T) {
	buildCalls := 0
	_, err := buildPlugin(buildOptions{
		Source:           "doublangu/plugins/official/sample",
		Target:           "server",
		ExplicitRevision: "not-a-revision",
		OutputDir:        t.TempDir(),
	}, buildIO{
		run: func(string, string, ...string) error {
			buildCalls++
			return nil
		},
	})
	if err == nil {
		t.Fatal("invalid explicit revision succeeded")
	}
	if buildCalls != 0 {
		t.Fatalf("invalid explicit revision invoked %d build commands, want zero", buildCalls)
	}
}

func TestBuildArgs_RaceSetting(t *testing.T) {
	args := strings.Join(buildArgs(buildOptions{Source: "example/plugin", BuildVCS: false, Race: true}, "output.so", "-X main.value=value"), " ")
	for _, required := range []string{"-buildmode=plugin", "-buildvcs=false", "-race", "-ldflags", "output.so", "example/plugin"} {
		if !strings.Contains(args, required) {
			t.Fatalf("build arguments %q omit %q", args, required)
		}
	}
}
