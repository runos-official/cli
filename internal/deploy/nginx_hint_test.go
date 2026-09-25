package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nonRootNginxDockerfile is the shape of the static test app: upstream
// nginx base, a non-root user, and a shipped nginx.conf that listens on
// an unprivileged port.
const nonRootNginxDockerfile = `FROM nginx:alpine
RUN addgroup -g 10001 app && adduser -D -u 10001 -G app appuser
COPY nginx.conf /etc/nginx/nginx.conf
COPY public /usr/share/nginx/html
RUN chown -R appuser:app /usr/share/nginx/html /var/cache/nginx /var/log/nginx /tmp
USER appuser
EXPOSE 3000
`

func TestNginxDockerfileHint(t *testing.T) {
	cases := []struct {
		name       string
		dockerfile string
		// want is "crash" for the root warning, "check" for the soft
		// warning, and "" for no hint.
		want string
	}{
		{name: "bare nginx:alpine is root", dockerfile: "FROM nginx:alpine\nCOPY dist /usr/share/nginx/html\n", want: "crash"},
		{name: "nginx:1.27-alpine is root", dockerfile: "FROM nginx:1.27-alpine\n", want: "crash"},
		{name: "nginx without tag is root", dockerfile: "FROM nginx\n", want: "crash"},
		{name: "platform flag is root", dockerfile: "FROM --platform=linux/amd64 nginx:alpine\n", want: "crash"},
		{name: "unprivileged variant", dockerfile: "FROM nginxinc/nginx-unprivileged:1.27-alpine\nEXPOSE 8080\n", want: ""},
		{name: "no FROM line", dockerfile: "# not a real Dockerfile\n", want: ""},
		{name: "non-nginx base", dockerfile: "FROM node:20-alpine\nWORKDIR /app\n", want: ""},
		{name: "token boundary", dockerfile: "FROM mynginxfork:1.0\n", want: ""},
		{name: "final stage nginx after build stage", dockerfile: "FROM node:20 AS build\nRUN echo hi\n\nFROM nginx:alpine\nCOPY --from=build /dist /usr/share/nginx/html\n", want: "crash"},
		{name: "non-root user with shipped config", dockerfile: nonRootNginxDockerfile, want: ""},
		{name: "numeric non-root user with sed listen", dockerfile: "FROM nginx:alpine\nRUN sed -i 's/listen  80;/listen 8080;/' /etc/nginx/conf.d/default.conf\nUSER 101\n", want: ""},
		{name: "explicit USER root", dockerfile: "FROM nginx:alpine\nUSER root\n", want: "crash"},
		{name: "USER 0:0 is root", dockerfile: "FROM nginx:alpine\nUSER 0:0\n", want: "crash"},
		{name: "last USER wins", dockerfile: "FROM nginx:alpine\nCOPY nginx.conf /etc/nginx/nginx.conf\nUSER appuser\nRUN true\nUSER root\n", want: "crash"},
		{name: "non-root user but default config", dockerfile: "FROM nginx:alpine\nUSER nginx\n", want: "check"},
		{name: "nginx only in a build stage", dockerfile: "FROM nginx:alpine AS conf\nRUN true\nFROM node:20-alpine\nCOPY --from=conf /etc/nginx /tmp/x\n", want: ""},
		{name: "final stage inherits USER from alias stage", dockerfile: "FROM nginx:alpine AS base\nCOPY nginx.conf /etc/nginx/nginx.conf\nUSER appuser\nFROM base\nCOPY public /usr/share/nginx/html\n", want: ""},
		{name: "final stage inherits root from alias stage", dockerfile: "FROM nginx:alpine AS base\nRUN true\nFROM base AS final\nCOPY public /usr/share/nginx/html\n", want: "crash"},
		{name: "USER in a build stage does not carry", dockerfile: "FROM node:20 AS build\nUSER node\nFROM nginx:alpine\nCOPY --from=build /dist /usr/share/nginx/html\n", want: "crash"},
		{name: "line continuation and comments", dockerfile: "# base\nFROM nginx:alpine\nRUN adduser -D app \\\n  && chown -R app /var/cache/nginx\n# switch\nUSER app\nCOPY default.conf /etc/nginx/conf.d/default.conf\n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Dockerfile")
			if err := os.WriteFile(path, []byte(tc.dockerfile), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			got := NginxDockerfileHint(path)
			switch tc.want {
			case "":
				if got != "" {
					t.Errorf("expected no hint, got: %s", got)
				}
			case "crash":
				if !strings.Contains(got, "CrashLoopBackOff") || !strings.Contains(got, "nginxinc/nginx-unprivileged") {
					t.Errorf("expected the root warning, got: %q", got)
				}
			case "check":
				if got == "" || strings.Contains(got, "CrashLoopBackOff") || !strings.Contains(got, "check") {
					t.Errorf("expected the soft check warning, got: %q", got)
				}
			}
		})
	}
}

func TestNginxDockerfileHint_UnreadableFile(t *testing.T) {
	if got := NginxDockerfileHint("/does/not/exist"); got != "" {
		t.Errorf("expected empty hint for missing file; got: %s", got)
	}
}
