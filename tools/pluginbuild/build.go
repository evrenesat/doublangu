package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"

	manifest "doublangu/internal/plugins"
	v1 "doublangu/pkg/pluginapi/v1"
)

type buildOptions struct {
	Source           string
	Target           string
	ExplicitRevision string
	OutputDir        string
	Name             string
	BuildVCS         bool
	Race             bool
	WorkingDir       string
}

type buildIO struct {
	run           func(dir, name string, args ...string) error
	readBuildInfo func(path string) (*debug.BuildInfo, error)
	checksum      func(path string) (string, error)
	makeDir       func(path string, perm os.FileMode) error
	writeFile     func(name string, data []byte, perm os.FileMode) error
	remove        func(name string) error
}

func defaultBuildIO(stdout, stderr io.Writer) buildIO {
	return buildIO{
		run: func(dir, name string, args ...string) error {
			command := exec.Command(name, args...)
			command.Dir = dir
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		readBuildInfo: manifest.ReadBuildInfo,
		checksum:      manifest.ComputeArtifactChecksum,
		makeDir:       os.MkdirAll,
		writeFile:     os.WriteFile,
		remove:        os.Remove,
	}
}

func buildPlugin(options buildOptions, io buildIO) (manifest.ArtifactReport, error) {
	if options.Source == "" {
		return manifest.ArtifactReport{}, fmt.Errorf("-src is required")
	}
	if err := validateExplicitRevision(options.ExplicitRevision); err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("invalid revision: %w", err)
	}
	if options.OutputDir == "" {
		return manifest.ArtifactReport{}, fmt.Errorf("-out must not be empty")
	}

	target, err := parseTarget(options.Target)
	if err != nil {
		return manifest.ArtifactReport{}, err
	}
	if err := io.makeDir(options.OutputDir, 0o755); err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("cannot create output directory %s: %w", options.OutputDir, err)
	}

	name := options.Name
	if name == "" {
		name = filepath.Base(options.Source)
	}
	artifactPath := filepath.Join(options.OutputDir, name+".so")
	sidecarPath := artifactPath + ".json"
	preBuildPath := filepath.Join(options.OutputDir, name+".pre.so")
	defer func() { _ = io.remove(preBuildPath) }()

	if err := io.run(options.WorkingDir, "go", buildArgs(options, preBuildPath, "")...); err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("pre-build: %w", err)
	}
	preBuildInfo, err := io.readBuildInfo(preBuildPath)
	if err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("read pre-build info: %w", err)
	}

	// The candidate artifact is the source of the VCS-precedence tier. This
	// happens only after a successful pre-build, so explicit invalid input can
	// never invoke go build.
	revision, err := resolveSourceRevision(options.ExplicitRevision, preBuildInfo, options.WorkingDir)
	if err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("resolve source revision: %w", err)
	}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("marshal target: %w", err)
	}
	preBuildSettings := manifest.ComputeBuildSettingsHash(preBuildInfo.Settings)
	preModuleGraph := manifest.ComputeModuleGraphHash(preBuildInfo)
	ldflags := fmt.Sprintf(
		"-X main.buildSettingsHash=%s -X main.moduleGraphHash=%s -X main.sourceRevision=%s -X main.targetJSON=%s",
		preBuildSettings, preModuleGraph, revision, targetJSON,
	)
	if err := io.run(options.WorkingDir, "go", buildArgs(options, artifactPath, ldflags)...); err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("final build: %w", err)
	}

	// Compute every report and sidecar fingerprint from the final binary, not
	// from the candidate build or a reconstructed expected value.
	finalBuildInfo, err := io.readBuildInfo(artifactPath)
	if err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("read final build info: %w", err)
	}
	checksum, err := io.checksum(artifactPath)
	if err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("compute artifact checksum: %w", err)
	}
	buildSettings := manifest.ComputeBuildSettingsHash(finalBuildInfo.Settings)
	moduleGraph := manifest.ComputeModuleGraphHash(finalBuildInfo)
	goos := buildSetting(finalBuildInfo.Settings, "GOOS")
	goarch := buildSetting(finalBuildInfo.Settings, "GOARCH")

	sidecar := v1.Manifest{
		ID:               name,
		Version:          "0.1.0",
		APIVersion:       v1.APIVersion,
		GoVersion:        v1.GoVersion,
		Target:           target,
		SourceRevision:   revision,
		ArtifactChecksum: checksum,
		BuildSettings:    buildSettings,
		ModuleGraph:      moduleGraph,
		Name:             name,
		Description:      name + " plugin (built by pluginbuild)",
		Author:           "Doublangu",
	}
	if err := sidecar.Validate(); err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("generated sidecar invalid: %w", err)
	}
	sidecarBytes, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("marshal sidecar: %w", err)
	}
	if err := io.writeFile(sidecarPath, append(sidecarBytes, '\n'), 0o644); err != nil {
		return manifest.ArtifactReport{}, fmt.Errorf("write sidecar: %w", err)
	}

	return manifest.ArtifactReport{
		Path:             artifactPath,
		ArtifactChecksum: checksum,
		BuildSettings:    buildSettings,
		ModuleGraph:      moduleGraph,
		SourceRevision:   revision,
		GOOS:             goos,
		GOARCH:           goarch,
	}, nil
}

func validateExplicitRevision(revision string) error {
	if revision == "" {
		return nil
	}
	_, err := manifest.DetermineSourceRevision(revision, nil, "")
	return err
}

func resolveSourceRevision(explicit string, candidate *debug.BuildInfo, workingDir string) (string, error) {
	return manifest.DetermineSourceRevision(explicit, candidate, workingDir)
}

func parseTarget(raw string) ([]string, error) {
	members := strings.Split(raw, ",")
	for index := range members {
		members[index] = strings.TrimSpace(members[index])
	}
	target, err := manifest.ParseTarget(members)
	if err != nil {
		return nil, fmt.Errorf("invalid target: %w", err)
	}
	return target, nil
}

func buildArgs(options buildOptions, output, ldflags string) []string {
	args := []string{"build", "-buildmode=plugin", fmt.Sprintf("-buildvcs=%t", options.BuildVCS)}
	if options.Race {
		args = append(args, "-race")
	}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	return append(args, "-o", output, options.Source)
}

func buildSetting(settings []debug.BuildSetting, key string) string {
	for _, setting := range settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return "unknown"
}
