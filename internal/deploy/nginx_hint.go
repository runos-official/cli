package deploy

import (
	"os"
	"regexp"
	"strings"
)

// nginxRootHint is the warning for a final stage that runs the upstream
// nginx image as root.
const nginxRootHint = "Hint: this Dockerfile uses the upstream `nginx:` image and runs it as root. " +
	"RunOS clusters reject containers running as root, and `nginx:` binds port 80 (requires root). " +
	"Static sites crash with CrashLoopBackOff at startup. " +
	"Use `nginxinc/nginx-unprivileged:<tag>` instead (listens on port 8080 as non-root, drop-in for static content)."

// nginxListenHint is the softer warning for a non-root final stage that
// shows no sign of a changed listen port.
const nginxListenHint = "Hint: this Dockerfile runs the upstream `nginx:` image as a non-root user. " +
	"The stock config listens on port 80, which a non-root user cannot bind. " +
	"If the pod does not start, check that your nginx config listens on a port above 1024, " +
	"or use `nginxinc/nginx-unprivileged:<tag>` (listens on port 8080)."

// upstreamNginxImage matches the official image reference, with or
// without a tag or digest, and excludes forks such as `mynginxfork` or
// `nginxinc/nginx-unprivileged`.
var upstreamNginxImage = regexp.MustCompile(`(?i)^(docker\.io/)?(library/)?nginx([:@].*)?$`)

// nginxListenEvidence matches an instruction that ships or edits the
// nginx config, which is how a Dockerfile moves nginx off port 80.
var nginxListenEvidence = regexp.MustCompile(`(?i)(/etc/nginx|\blisten\b)`)

// dockerStage is one FROM block of a Dockerfile.
type dockerStage struct {
	base  string
	alias string
	// user is the last USER value in the stage, "" when the stage sets none.
	user string
	// touchesConfig is true when a COPY, ADD or RUN in the stage writes
	// the nginx config or changes a listen directive.
	touchesConfig bool
}

// NginxDockerfileHint returns a one-line stderr hint when the image that
// runs (the final stage) is the upstream nginx image in a shape that
// cannot start on RunOS. It returns the root warning when the final stage
// runs as root, the softer check warning when it runs as non-root but
// leaves the stock config, and "" otherwise. A stage that starts FROM an
// earlier alias inherits that stage's base, USER and config changes.
//
// Non-blocking: deploy proceeds either way. The file is unreadable or has
// no FROM line: the result is "".
func NginxDockerfileHint(dockerfilePath string) string {
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return ""
	}
	stages := parseDockerStages(string(data))
	if len(stages) == 0 {
		return ""
	}
	final := resolveStage(stages, len(stages)-1)
	if !upstreamNginxImage.MatchString(final.base) {
		return ""
	}
	if isRootUser(final.user) {
		return nginxRootHint
	}
	if final.touchesConfig {
		return ""
	}
	return nginxListenHint
}

// parseDockerStages splits Dockerfile text into stages. It joins
// backslash continuations and skips comment lines.
func parseDockerStages(text string) []dockerStage {
	var stages []dockerStage
	for _, line := range dockerInstructions(text) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		keyword := strings.ToUpper(fields[0])
		if keyword == "FROM" {
			stages = append(stages, parseFrom(fields[1:]))
			continue
		}
		if len(stages) == 0 {
			continue
		}
		cur := &stages[len(stages)-1]
		switch keyword {
		case "USER":
			if len(fields) > 1 {
				cur.user = fields[1]
			}
		case "COPY", "ADD", "RUN":
			if nginxListenEvidence.MatchString(line) {
				cur.touchesConfig = true
			}
		}
	}
	return stages
}

// dockerInstructions returns one string per Dockerfile instruction.
func dockerInstructions(text string) []string {
	var out []string
	var buf strings.Builder
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			buf.WriteString(strings.TrimSuffix(line, "\\"))
			buf.WriteString(" ")
			continue
		}
		buf.WriteString(line)
		out = append(out, buf.String())
		buf.Reset()
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// parseFrom reads the arguments of a FROM instruction: optional --flags,
// the image, then an optional `AS <alias>`.
func parseFrom(args []string) dockerStage {
	var st dockerStage
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			continue
		}
		if st.base == "" {
			st.base = a
			continue
		}
		if strings.EqualFold(a, "AS") && i+1 < len(args) {
			st.alias = args[i+1]
			break
		}
	}
	return st
}

// resolveStage folds a stage onto the earlier stage its FROM names, so
// the result carries the real base image and the inherited USER.
func resolveStage(stages []dockerStage, idx int) dockerStage {
	st := stages[idx]
	for i := idx - 1; i >= 0; i-- {
		if stages[i].alias == "" || !strings.EqualFold(stages[i].alias, st.base) {
			continue
		}
		parent := resolveStage(stages, i)
		st.base = parent.base
		if st.user == "" {
			st.user = parent.user
		}
		st.touchesConfig = st.touchesConfig || parent.touchesConfig
		break
	}
	return st
}

// isRootUser reports whether a USER value runs as root. An absent USER
// means the image default, which is root for upstream nginx.
func isRootUser(user string) bool {
	if user == "" {
		return true
	}
	name := strings.SplitN(user, ":", 2)[0]
	return name == "root" || name == "0"
}
