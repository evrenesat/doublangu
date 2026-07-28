// Command pluginbuild compiles a native Go plugin and writes its matching
// sidecar manifest. It reports fields read from the final artifact and sidecar.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	source := flag.String("src", "", "Source package import path (required)")
	target := flag.String("target", "server,agent", "Comma-separated target roles: server, agent, or server,agent")
	revision := flag.String("revision", "", "Explicit source revision (7-64 hex or 'unknown')")
	output := flag.String("out", "bin/plugins", "Output directory for .so and .so.json")
	name := flag.String("name", "", "Plugin file base name (defaults to last element of -src)")
	buildVCS := flag.Bool("buildvcs", true, "Pass -buildvcs to go build for VCS stamping")
	race := flag.Bool("race", false, "Build the plugin with the race detector")
	flag.Parse()

	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluginbuild: get working directory: %v\n", err)
		os.Exit(1)
	}
	report, err := buildPlugin(buildOptions{
		Source:           *source,
		Target:           *target,
		ExplicitRevision: *revision,
		OutputDir:        *output,
		Name:             *name,
		BuildVCS:         *buildVCS,
		Race:             *race,
		WorkingDir:       workingDir,
	}, defaultBuildIO(os.Stdout, os.Stderr))
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluginbuild: %v\n", err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluginbuild: marshal report: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
