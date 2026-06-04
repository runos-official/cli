package deploy

import "testing"

// TestExcludeFromTarball pins the archive-membership rules for both option
// sets. The app-deploy archive strips dotfiles and RunOS-managed manifests
// on top of .dockerignore; the build-context archive (CreateBuildContextTarball)
// keeps them, since a generic Dockerfile may COPY a dotfile or a runos.yaml.
// Both honor .dockerignore and prune matched directories.
func TestExcludeFromTarball(t *testing.T) {
	appDeploy := tarballOptions{excludeHidden: true, excludeRunosManaged: true}
	buildCtx := tarballOptions{}
	patterns := []string{"node_modules", "*.log"}

	cases := []struct {
		name    string
		relPath string
		isDir   bool
		opts    tarballOptions
		want    bool // true = excluded
	}{
		// App-deploy: dotfiles and runos manifests are stripped.
		{"app-deploy hides dotfile", ".env", false, appDeploy, true},
		{"app-deploy hides dotdir", ".git", true, appDeploy, true},
		{"app-deploy hides .dockerignore", ".dockerignore", false, appDeploy, true},
		{"app-deploy strips runos.yaml", "runos.yaml", false, appDeploy, true},
		{"app-deploy strips runos.mycluster.appid6.yaml", "runos.mycluster.appid6.yaml", false, appDeploy, true},
		{"app-deploy strips overrides dir", "overrides", true, appDeploy, true},
		{"app-deploy keeps source", "src/main.go", false, appDeploy, false},
		{"app-deploy keeps Dockerfile", "Dockerfile", false, appDeploy, false},

		// Build-context: dotfiles and runos manifests are KEPT.
		{"build-ctx keeps dotfile", ".env", false, buildCtx, false},
		{"build-ctx keeps dotdir", ".config", true, buildCtx, false},
		{"build-ctx keeps .dockerignore", ".dockerignore", false, buildCtx, false},
		{"build-ctx keeps runos.yaml", "runos.yaml", false, buildCtx, false},
		{"build-ctx keeps overrides dir", "overrides", true, buildCtx, false},
		{"build-ctx keeps source", "src/main.go", false, buildCtx, false},
		{"build-ctx keeps Dockerfile", "Dockerfile", false, buildCtx, false},

		// .dockerignore is honored in BOTH modes.
		{"app-deploy honors dockerignore dir", "node_modules", true, appDeploy, true},
		{"build-ctx honors dockerignore dir", "node_modules", true, buildCtx, true},
		{"app-deploy honors dockerignore glob", "debug.log", false, appDeploy, true},
		{"build-ctx honors dockerignore glob", "debug.log", false, buildCtx, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := excludeFromTarball(tc.relPath, tc.isDir, patterns, tc.opts)
			if got != tc.want {
				t.Errorf("excludeFromTarball(%q, isDir=%v, %+v) = %v, want %v",
					tc.relPath, tc.isDir, tc.opts, got, tc.want)
			}
		})
	}
}
