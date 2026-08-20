package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/presentation"
)

type doctorRuntimeView struct {
	runtime              domain.Runtime
	capabilities         int
	findings             []domain.Finding
	duplicates           []analysis.DuplicateName
	discoveryUnavailable bool
	duplicateCheckError  bool
	compatibility        compatibilityDiagnostic
}

func doctorFindingCount(view doctorRuntimeView) int {
	return len(view.findings) + len(view.duplicates)
}

func renderDoctorView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, runtimes []doctorRuntimeView) {
	ordered := append([]doctorRuntimeView(nil), runtimes...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return runtimeOrder(ordered[left].runtime) < runtimeOrder(ordered[right].runtime)
	})

	fmt.Fprintln(out, "Doctor")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Runtime overview")
	rows := make([][]string, 0, len(ordered))
	for _, runtime := range ordered {
		rows = append(rows, []string{
			renderer.Runtime(string(runtime.runtime)),
			doctorRuntimeStatus(renderer, runtime),
			renderer.Integer(int64(runtime.capabilities)),
			renderer.Integer(int64(doctorFindingCount(runtime))),
		})
	}
	if table := reportTable(renderer, []string{"Runtime", "Status", "Capabilities", "Findings"}, rows); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}

	findings := doctorFindings(ordered)
	if len(findings) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Findings")
		for _, finding := range findings {
			renderDoctorFinding(out, renderer, verbose, finding)
		}
	}

	if verbose {
		fmt.Fprintln(out)
		renderDoctorCompatibility(out, renderer, ordered)
	}

	fmt.Fprintln(out)
	if len(findings) == 0 && !doctorHasUnavailable(ordered) {
		fmt.Fprintln(out, "No configuration problems found.")
	} else if doctorHasUnavailable(ordered) {
		fmt.Fprintln(out, "Doctor could not complete every runtime discovery check.")
	} else {
		fmt.Fprintf(out, "%s require attention.\n", humanCount(renderer, len(findings), "finding", "findings"))
	}
}

func doctorRuntimeStatus(renderer presentation.HumanRenderer, runtime doctorRuntimeView) string {
	status := "✓ Healthy"
	style := presentation.StyleSuccess
	if runtime.discoveryUnavailable {
		status = "✗ Unavailable"
		style = presentation.StyleError
	} else if runtime.duplicateCheckError || doctorFindingCount(runtime) > 0 {
		status = "! Needs attention"
		style = presentation.StyleWarning
	}
	return renderer.Colorize(status, style)
}

type doctorFindingView struct {
	runtime        domain.Runtime
	code           string
	severity       domain.Severity
	confidence     domain.Confidence
	capabilityType domain.CapabilityType
	capabilityName string
	message        string
	definitions    []domain.Capability
	duplicate      bool
}

func doctorFindings(runtimes []doctorRuntimeView) []doctorFindingView {
	result := make([]doctorFindingView, 0)
	for _, runtime := range runtimes {
		for _, finding := range runtime.findings {
			result = append(result, doctorFindingView{
				runtime:        finding.Runtime,
				code:           finding.Code,
				severity:       finding.Severity,
				confidence:     finding.Confidence,
				capabilityType: finding.CapabilityType,
				capabilityName: finding.CapabilityName,
				message:        finding.Message,
			})
		}
		for _, duplicate := range runtime.duplicates {
			result = append(result, doctorFindingView{
				runtime:        duplicate.Runtime,
				code:           "duplicate-capability",
				severity:       domain.SeverityWarning,
				confidence:     domain.ConfidenceObserved,
				capabilityType: duplicate.CapabilityType,
				capabilityName: duplicate.Name,
				definitions:    append([]domain.Capability(nil), duplicate.Definitions...),
				duplicate:      true,
			})
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].runtime != result[right].runtime {
			return runtimeOrder(result[left].runtime) < runtimeOrder(result[right].runtime)
		}
		if result[left].code != result[right].code {
			return result[left].code < result[right].code
		}
		if result[left].capabilityType != result[right].capabilityType {
			return result[left].capabilityType < result[right].capabilityType
		}
		return result[left].capabilityName < result[right].capabilityName
	})
	return result
}

func renderDoctorFinding(out io.Writer, renderer presentation.HumanRenderer, verbose bool, finding doctorFindingView) {
	marker := "!"
	style := presentation.StyleWarning
	switch finding.severity {
	case domain.SeverityError:
		marker = "✗"
		style = presentation.StyleError
	case domain.SeverityInfo:
		marker = "-"
		style = presentation.StyleMuted
	}
	identity := renderer.Runtime(string(finding.runtime))
	if finding.capabilityType.Valid() {
		identity += " · " + renderer.Type(string(finding.capabilityType))
	}
	if strings.TrimSpace(finding.capabilityName) != "" {
		identity += " · " + cleanText(finding.capabilityName)
	}
	if finding.duplicate {
		identity += fmt.Sprintf(" · %s definitions", renderer.Integer(int64(len(finding.definitions))))
	}
	writeReportText(out, renderer, fmt.Sprintf("%s %s", renderer.Colorize(marker, style), doctorFindingTitle(finding.code)), 2)
	writeReportText(out, renderer, identity, 4)
	if !finding.duplicate && strings.TrimSpace(finding.message) != "" {
		writeReportText(out, renderer, cleanText(finding.message), 4)
	}
	if finding.confidence != "" && finding.confidence != domain.ConfidenceObserved && !finding.duplicate {
		writeReportText(out, renderer, "Confidence: "+presentation.StatusWord(string(finding.confidence)), 4)
	}
	if verbose && finding.duplicate && len(finding.definitions) > 0 {
		fmt.Fprintln(out, "    Definitions")
		for _, definition := range finding.definitions {
			source := renderer.Path(cleanText(definition.Source))
			if source == "" {
				source = "Source unavailable"
			}
			writeReportText(out, renderer, source+" · "+scopeLabel(definition.Scope), 6)
		}
	}
}

func doctorFindingTitle(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "malformed-agent-toml":
		return "Malformed agent TOML"
	case "malformed-agent-metadata":
		return "Malformed agent metadata"
	case "malformed-skill-metadata":
		return "Malformed Skill metadata"
	case "malformed-frontmatter":
		return "Malformed metadata"
	case "mcp-command-unresolved", "unresolved-mcp-command":
		return "Unresolved MCP command"
	case "duplicate-capability", "duplicate-capability-name", "duplicate-capability-source":
		return "Duplicate capability"
	}
	title := presentation.StatusWord(code)
	return strings.NewReplacer("Toml", "TOML", "Json", "JSON").Replace(title)
}

func renderDoctorCompatibility(out io.Writer, renderer presentation.HumanRenderer, runtimes []doctorRuntimeView) {
	fmt.Fprintln(out, "Compatibility")
	for _, runtime := range runtimes {
		value := compatibilityVersion(runtime.compatibility)
		status := "unknown"
		if runtime.compatibility.hasEvaluation {
			status = string(runtime.compatibility.evaluation.State)
		}
		fmt.Fprintf(out, "  %s\n", renderer.Runtime(string(runtime.runtime)))
		details := []presentation.KeyValue{
			{Key: "Runtime version", Value: value},
			{Key: "Latest validated", Value: cleanText(runtime.compatibility.latestValidated)},
			{Key: "Compatibility", Value: renderer.Status(status)},
			{Key: "Detection", Value: cleanText(string(runtime.compatibility.detection.Status))},
			{Key: "Validation basis", Value: cleanText(runtime.compatibility.validationBasis)},
		}
		fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, details, 4), 4))
	}
}

func doctorHasUnavailable(runtimes []doctorRuntimeView) bool {
	for _, runtime := range runtimes {
		if runtime.discoveryUnavailable || runtime.duplicateCheckError {
			return true
		}
	}
	return false
}
