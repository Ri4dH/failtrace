package report

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Ri4dH/failtrace/internal/plan"
)

var verdictText = map[plan.Verdict]string{
	plan.VerdictReady:   "READY: nothing known to be missing",
	plan.VerdictPartial: "PARTIAL: can run, but the warnings below may make it differ from CI",
	plan.VerdictBlocked: "BLOCKED: the failing step cannot be replayed yet",
}

// WritePlan renders a reproduction plan.
func WritePlan(w io.Writer, p *plan.Plan, opts TextOptions) error {
	c := painter{on: opts.Color}
	var b strings.Builder

	fmt.Fprintf(&b, "%s %s\n", c.bold("Reproduction plan"), c.dim("· "+p.Repository))
	job := p.Job.Name
	var where []string
	if p.Job.WorkflowJob != "" {
		where = append(where, "jobs."+p.Job.WorkflowJob)
	}
	if len(p.Job.Matrix) > 0 {
		where = append(where, formatMap(p.Job.Matrix, ", "))
	}
	if len(where) > 0 {
		job += "  " + c.dim("("+strings.Join(where, " · ")+")")
	}
	row(&b, "Job", job)
	row(&b, "Verdict", verdictColor(c, p.Verdict)(verdictText[p.Verdict]))
	img := p.Image.Ref + "  " + c.dim(p.Image.Platform)
	if p.Image.Exact {
		img += "  " + c.green("exact")
	}
	row(&b, "Image", img)
	row(&b, "", c.dim(p.Image.Source))
	row(&b, "Commit", short(p.Commit))
	row(&b, "Workspace", p.Workspace)

	if len(p.Toolchains) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", c.bold("Toolchains"))
		for _, t := range p.Toolchains {
			fmt.Fprintf(&b, "    %-8s %-10s %s\n", t.Tool, orDash(t.Version), c.dim(toolchainDetail(t)))
		}
	}
	if len(p.Services) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", c.bold("Services"))
		for _, s := range p.Services {
			extra := ""
			if len(s.Ports) > 0 {
				extra = "ports " + strings.Join(s.Ports, ", ")
			}
			fmt.Fprintf(&b, "    %-10s %s  %s\n", s.Name, s.Image, c.dim(extra))
		}
	}
	writePlanEnv(&b, c, p)
	writePlanSteps(&b, c, p)

	if len(p.Issues) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", c.bold("Issues"))
		for _, is := range p.Issues {
			icon := map[plan.Severity]string{plan.SeverityBlocker: c.red("✗"), plan.SeverityWarning: c.yellow("!"), plan.SeverityInfo: c.cyan("i")}[is.Severity]
			where := ""
			if is.Step > 0 {
				where = c.dim(fmt.Sprintf("[step %d] ", is.Step))
			}
			fmt.Fprintf(&b, "    %s %s%s\n", icon, where, shortenRefs(is.Message))
		}
	}
	fmt.Fprintf(&b, "\n  %s\n", c.dim("Dry run: nothing was executed."))
	_, err := io.WriteString(w, b.String())
	return err
}

func writePlanEnv(b *strings.Builder, c painter, p *plan.Plan) {
	var own, defaults []plan.EnvVar
	for _, e := range p.Env {
		if e.Source == "github" {
			defaults = append(defaults, e)
		} else {
			own = append(own, e)
		}
	}
	fmt.Fprintf(b, "\n  %s\n", c.bold("Environment"))
	for _, e := range own {
		val := e.Value
		if e.Secret {
			val = c.yellow("<secret>")
		}
		fmt.Fprintf(b, "    %s=%s  %s\n", e.Name, val, c.dim(e.Source))
	}
	if len(defaults) > 0 {
		names := make([]string, 0, 4)
		for _, e := range defaults[:min(4, len(defaults))] {
			names = append(names, e.Name)
		}
		fmt.Fprintf(b, "    %s\n", c.dim(fmt.Sprintf("+ %d GitHub defaults (%s, …; see --json)", len(defaults), strings.Join(names, ", "))))
	}
}

func writePlanSteps(b *strings.Builder, c painter, p *plan.Plan) {
	fmt.Fprintf(b, "\n  %s\n", c.bold("Steps"))
	const pad = "                    "
	for i, s := range p.Steps {
		indent := ""
		if s.Parent > 0 {
			indent = "  "
		}
		kind := fmt.Sprintf("%-11s", s.Kind)
		switch s.Kind {
		case plan.KindSkip, plan.KindUnsupported:
			kind = c.dim(kind)
		case plan.KindRun:
			kind = c.cyan(kind)
		}
		name := shortenRefs(s.Name)
		lines := strings.Split(strings.TrimRight(s.Script, "\n"), "\n")
		defaultName := s.Kind == plan.KindRun && s.Name == "Run "+lines[0]
		if defaultName {
			name = c.cyan("$") + " " + lines[0]
			if len(lines) > 1 && !s.Target {
				name += c.dim(fmt.Sprintf("  (+%d lines)", len(lines)-1))
			}
		}
		if s.Target {
			name = c.red(name + "  ← failed in CI")
		}
		fmt.Fprintf(b, "   %2d  %s%s %s\n", i+1, indent, kind, name)

		detail := func(format string, args ...any) {
			fmt.Fprintf(b, "%s%s%s\n", pad, indent, fmt.Sprintf(format, args...))
		}
		switch s.Kind {
		case plan.KindRun:
			if s.Target {
				shown := lines
				if defaultName {
					shown = lines[1:]
				}
				for _, l := range shown {
					detail("%s %s", c.cyan("$"), l)
				}
				detail("%s", c.dim("shell: "+s.Shell))
				if s.WorkingDirectory != "" {
					detail("%s", c.dim("working-directory: "+s.WorkingDirectory))
				}
				if len(s.Env) > 0 {
					detail("%s", c.dim("env: "+formatMap(s.Env, " ")))
				}
			} else if !defaultName {
				more := ""
				if len(lines) > 1 {
					more = c.dim(fmt.Sprintf("  (+%d lines)", len(lines)-1))
				}
				detail("%s %s%s", c.cyan("$"), lines[0], more)
			}
		case plan.KindCheckout:
			co := s.Checkout
			text := co.Repository + " @ " + short(co.Commit)
			if co.MergeHead != "" {
				text += fmt.Sprintf(" (merge of %s into %s)", short(co.MergeHead), short(co.MergeBase))
			}
			if co.Path != "" {
				text += " → " + co.Path
			}
			detail("%s", c.dim(text))
		case plan.KindToolchain:
			detail("%s", c.dim("→ install "+s.Toolchain.Tool+" "+orDash(s.Toolchain.Version)))
		default:
			if s.Note != "" {
				detail("%s", c.dim("— "+shortenRefs(s.Note)))
			}
		}
	}
}

func toolchainDetail(t plan.Toolchain) string {
	var parts []string
	if t.Exact {
		parts = append(parts, "exact")
	}
	if t.Spec != "" && t.Spec != t.Version {
		parts = append(parts, fmt.Sprintf("asked for %q", t.Spec))
	}
	parts = append(parts, "from "+shortenRefs(t.Source))
	return strings.Join(parts, " · ")
}

func verdictColor(c painter, v plan.Verdict) func(string) string {
	switch v {
	case plan.VerdictReady:
		return c.green
	case plan.VerdictBlocked:
		return c.red
	}
	return c.yellow
}

func row(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "  %-10s %s\n", label, value)
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

var pinnedRefRe = regexp.MustCompile(`@([0-9a-f]{7})[0-9a-f]{33}\b`)

// shortenRefs abbreviates actions pinned to full commit SHAs for display.
func shortenRefs(s string) string {
	return pinnedRefRe.ReplaceAllString(s, "@$1")
}
