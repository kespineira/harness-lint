package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/kespineira/harness-lint/internal/compatibility"
	"github.com/kespineira/harness-lint/internal/domain"
)

// compatibilityPolicy is deliberately owned by the CLI diagnostic boundary.
// The version facts are the accepted compatibility baselines; they are not
// inferred from a local installation or persisted with captured events.
func compatibilityPolicy(runtime domain.Runtime) (compatibility.Policy, string) {
	switch runtime {
	case domain.RuntimeClaudeCode:
		return compatibility.Policy{Runtime: runtime}, "synthetic runtime-conformance fixtures; no live runtime version validated"
	case domain.RuntimeCodex:
		return compatibility.Policy{Runtime: runtime}, "synthetic runtime-conformance fixtures; no live runtime version validated"
	default:
		return compatibility.Policy{Runtime: runtime}, "no compatibility basis"
	}
}

func versionDetector(config commandConfig) compatibility.Detector {
	resolver := config.versionResolver
	if resolver == nil {
		lookPath := config.lookPath
		if lookPath == nil {
			lookPath = exec.LookPath
		}
		resolver = compatibility.ResolveFunc(lookPath)
	}
	runner := config.versionRunner
	if runner == nil {
		runner = compatibility.RunFunc(runVersionCommand)
	}
	return compatibility.Detector{Resolver: resolver, Runner: runner}
}

func runVersionCommand(ctx context.Context, executable string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.Output()
	if err != nil {
		// The detector deliberately maps command errors to a bounded status and
		// never exposes this output to users or persistent storage.
		return "", err
	}
	return string(output), nil
}

type compatibilityDiagnostic struct {
	detection       compatibility.Detection
	evaluation      compatibility.Compatibility
	hasEvaluation   bool
	latestValidated string
	validationBasis string
}

func detectCompatibility(ctx context.Context, config commandConfig, runtime domain.Runtime) compatibilityDiagnostic {
	policy, basis := compatibilityPolicy(runtime)
	detection := versionDetector(config).Detect(ctx, runtime)
	result := compatibilityDiagnostic{
		detection:       detection,
		latestValidated: latestValidatedVersion(policy),
		validationBasis: basis,
	}
	if detection.Version != nil {
		result.evaluation = compatibility.Evaluate(policy, *detection.Version)
		result.hasEvaluation = true
	}
	return result
}

func latestValidatedVersion(policy compatibility.Policy) string {
	if len(policy.Validated) == 0 {
		return "unknown"
	}
	latest := policy.Validated[0].Version
	for _, fact := range policy.Validated[1:] {
		if fact.Version.Compare(latest) > 0 {
			latest = fact.Version
		}
	}
	return latest.String()
}

func printCompatibilityDiagnostic(out io.Writer, diagnostic compatibilityDiagnostic) {
	version := "unknown"
	status := string(compatibility.StateUnknown)
	if diagnostic.detection.Version != nil {
		version = diagnostic.detection.Version.String()
	}
	if diagnostic.hasEvaluation {
		status = string(diagnostic.evaluation.State)
	}
	fmt.Fprintf(out, "compatibility runtime=%s detected-version=%s latest-validated=%s validation-basis=%s status=%s detection=%s\n",
		diagnostic.detection.Runtime,
		version,
		diagnostic.latestValidated,
		cleanText(diagnostic.validationBasis),
		status,
		diagnostic.detection.Status)
}
