// Package policy holds recom's built-in file selection defaults. The same
// lists drive the rsync upload filter on the client and output detection on
// the helper. All patterns use pathmatch (rsync-style) syntax.
package policy

// Internal paths are never uploaded or collected, whatever the configuration.
var Internal = []string{
	"/.recom/",
}

// Secrets are never uploaded or downloaded by default. Users can opt specific
// paths back in with [sync] include / [outputs] include.
var Secrets = []string{
	".env",
	".env.*",
	".envrc",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"*.keystore",
	"*.jks",
	"id_rsa*",
	"id_dsa*",
	"id_ecdsa*",
	"id_ed25519*",
	".ssh/",
	".gnupg/",
	".aws/",
	".azure/",
	".kube/",
	".docker/",
	".config/gcloud/",
	".netrc",
	".git-credentials",
	".npmrc",
	".pypirc",
	"credentials.json",
	"service-account*.json",
	"*.tfstate",
	"*.tfstate.*",
	".terraform/",
	".vault-token",
}

// SecretAllow are secret-looking names that are conventionally safe templates.
var SecretAllow = []string{
	".env.example",
	".env.sample",
	".env.template",
	".env.dist",
}

// UploadExcludes are the default upload excludes: VCS metadata, local
// virtualenvs (macOS binaries are useless on Linux), caches, editor state, and
// directories that hold remote-generated ML artifacts.
var UploadExcludes = []string{
	".git/",
	".hg/",
	".svn/",
	".venv/",
	"venv/",
	".conda/",
	"__pycache__/",
	"*.pyc",
	".mypy_cache/",
	".pytest_cache/",
	".ruff_cache/",
	".tox/",
	".nox/",
	".ipynb_checkpoints/",
	"node_modules/",
	".idea/",
	".vscode/",
	".DS_Store",
	"*.swp",
	"._*",
	".cache/",
	"wandb/",
	"mlruns/",
	"lightning_logs/",
	"checkpoints/",
}

// OutputExcludes are changed paths that are never treated as run outputs by
// default (caches and environments that tools rewrite constantly).
var OutputExcludes = []string{
	".git/",
	".hg/",
	".svn/",
	".venv/",
	"venv/",
	".conda/",
	"__pycache__/",
	"*.pyc",
	".mypy_cache/",
	".pytest_cache/",
	".ruff_cache/",
	".tox/",
	".nox/",
	".ipynb_checkpoints/",
	"node_modules/",
	".cache/",
	"wandb/",
	"*.swp",
	"*.tmp",
	".nfs*",
}

// Default size limits for automatic output download.
const (
	DefaultMaxFileSize = 100 << 20 // 100 MiB per file
	DefaultMaxRunSize  = 1 << 30   // 1 GiB per run
	DefaultHashLimit   = 256 << 20 // files above this are not hashed pre-run
)
