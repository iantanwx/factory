package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// HostConfigFile is the name of the host config file in the instance
// directory.
const HostConfigFile = "host.yaml"

// HostScope is the reserved secret-store project that holds the platform
// credentials. No project id can take it, because a project id starts with a
// lower-case letter.
const HostScope = "_host"

// GlobalScope is the reserved secret-store project that holds the values
// every project may use. A run gets one only when its project's manifest
// names it, and a project's own value wins.
const GlobalScope = "_global"

// HostConfig is the instance scope of R35. It names each platform credential
// by its secret-store name and holds no credential value (KTD15, KTD17).
type HostConfig struct {
	StateDir    string          `yaml:"state_dir"`
	Store       StoreConfig     `yaml:"store,omitempty"`
	KeyFile     string          `yaml:"key_file,omitempty"`
	AgentHost   AgentHostConfig `yaml:"agent_host,omitempty"`
	Beads       BeadsConfig     `yaml:"beads,omitempty"`
	Harnesses   []HarnessLogin  `yaml:"harnesses,omitempty"`
	Credentials Credentials     `yaml:"credentials,omitempty"`
	Vaults      []string        `yaml:"vaults,omitempty"` // 1Password vaults that secret import may read (KTD28)
	// Throughput holds a cap and a schedule for every stage, native job and
	// pool (KTD30). Project config may override a schedule only.
	Throughput  map[string]Throughput `yaml:"throughput"`
	RunBudget   time.Duration         `yaml:"run_budget,omitempty"`   // default run budget (KTD9)
	QuietPeriod time.Duration         `yaml:"quiet_period,omitempty"` // quiet period before the server acts
	Projects    []Project             `yaml:"projects,omitempty"`
}

// StoreConfig names the database and the identity of the instance.
type StoreConfig struct {
	Path     string `yaml:"path,omitempty"`
	Identity string `yaml:"identity,omitempty"`
}

// AgentHostConfig selects the agent host implementation. CheckoutRoot is
// the directory that holds every checkout the host makes; the host refuses
// a path outside it.
type AgentHostConfig struct {
	Kind         string `yaml:"kind,omitempty"`
	Binary       string `yaml:"binary,omitempty"`
	CheckoutRoot string `yaml:"checkout_root,omitempty"`
}

// BeadsConfig names the pinned bd and dolt binaries. With both set, the
// server keeps each project's record of work in beads (KTD3).
type BeadsConfig struct {
	BD   string `yaml:"bd,omitempty"`
	Dolt string `yaml:"dolt,omitempty"`
}

// HarnessLogin names a harness and where its login lives. The login itself
// stays in the harness's own directory.
type HarnessLogin struct {
	Name      string `yaml:"name"`
	Binary    string `yaml:"binary,omitempty"`
	ConfigDir string `yaml:"config_dir,omitempty"`
}

// Credentials names each platform credential by its name in the secret store
// under HostScope. Each *Secret field holds a store name, never a value.
type Credentials struct {
	// GitHubApp is the one App of a host from before Apps had owners. Its
	// owner is the account that GitHub says owns it.
	GitHubApp GitHubAppCredentials `yaml:"github_app,omitempty"`
	// GitHubApps holds one private App for each GitHub owner, the operator's
	// account or an organisation. A private App installs only on its
	// owner's repositories, so each owner of a project needs its own.
	GitHubApps         []GitHubAppCredentials        `yaml:"github_apps,omitempty"`
	MergifyTokenSecret string                        `yaml:"mergify_token_secret,omitempty"`
	Trackers           map[string]TrackerCredentials `yaml:"trackers,omitempty"`
}

// GitHubAppCredentials identifies one loop App. The App id is public. The
// private key lives in the encrypted store.
type GitHubAppCredentials struct {
	// Owner is the GitHub account that owns the App. Empty only in the
	// legacy github_app entry.
	Owner            string `yaml:"owner,omitempty"`
	AppID            int64  `yaml:"app_id,omitempty"`
	PrivateKeySecret string `yaml:"private_key_secret,omitempty"`
}

// Apps lists every loop App: the legacy entry first, when it names one,
// then one for each owner.
func (c Credentials) Apps() []GitHubAppCredentials {
	var out []GitHubAppCredentials
	if c.GitHubApp.AppID != 0 {
		out = append(out, c.GitHubApp)
	}
	return append(out, c.GitHubApps...)
}

// App returns the App of owner. The legacy entry has no owner, so it
// answers only by its id; ok is false when no App names owner.
func (c Credentials) App(owner string) (GitHubAppCredentials, bool) {
	for _, a := range c.Apps() {
		if a.Owner != "" && strings.EqualFold(a.Owner, owner) {
			return a, true
		}
	}
	return GitHubAppCredentials{}, false
}

// SetApp records the App of a.Owner. It replaces the owner's entry, or the
// legacy entry when it holds the same App id.
func (c *Credentials) SetApp(a GitHubAppCredentials) {
	if c.GitHubApp.AppID != 0 && c.GitHubApp.AppID == a.AppID {
		c.GitHubApp = GitHubAppCredentials{}
	}
	for i, b := range c.GitHubApps {
		if strings.EqualFold(b.Owner, a.Owner) || b.AppID == a.AppID {
			c.GitHubApps[i] = a
			return
		}
	}
	c.GitHubApps = append(c.GitHubApps, a)
}

// TrackerCredentials names one tracker adapter's token in the store.
type TrackerCredentials struct {
	TokenSecret string `yaml:"token_secret"`
}

// CredentialRef is one platform credential that host config names.
type CredentialRef struct {
	Purpose string // for example "github_app.private_key"
	Name    string // the secret-store name under HostScope
}

// ParseHost parses and validates host config.
func ParseHost(data []byte) (*HostConfig, error) {
	var cfg HostConfig
	if err := decodeStrict(HostConfigFile, data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadHost reads and parses the host config file at path.
func LoadHost(path string) (*HostConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read host config: %w", err)
	}
	return ParseHost(data)
}

// SaveHost validates cfg and writes it to path atomically at mode 0600.
func SaveHost(path string, cfg *HostConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: encode host config: %w", err)
	}
	return WriteFileAtomic(path, data, 0o600)
}

// Validate refuses a host config value that the server cannot use.
func (c *HostConfig) Validate() error {
	ck := &checker{source: HostConfigFile}
	if !filepath.IsAbs(c.StateDir) {
		ck.fail("state_dir", "must be an absolute path")
	}
	for _, f := range [][2]string{
		{"store.path", c.Store.Path},
		{"key_file", c.KeyFile},
		{"agent_host.binary", c.AgentHost.Binary},
		{"agent_host.checkout_root", c.AgentHost.CheckoutRoot},
		{"beads.bd", c.Beads.BD},
		{"beads.dolt", c.Beads.Dolt},
	} {
		if f[1] != "" && !filepath.IsAbs(f[1]) {
			ck.fail(f[0], "must be an absolute path")
		}
	}
	if c.KeyFile != "" && c.KeyFile == c.Store.Path {
		ck.fail("key_file", "must not be the database file")
	}
	if c.AgentHost.Kind != "" && !validIdent(c.AgentHost.Kind) {
		ck.fail("agent_host.kind", "must be a lower-case identifier")
	}
	seen := map[string]bool{}
	for i, h := range c.Harnesses {
		f := fmt.Sprintf("harnesses[%d]", i)
		if !validIdent(h.Name) {
			ck.fail(f+".name", "must be a lower-case identifier")
		} else if seen[h.Name] {
			ck.fail(f+".name", "names a harness twice")
		}
		seen[h.Name] = true
		for _, sub := range [][2]string{{"binary", h.Binary}, {"config_dir", h.ConfigDir}} {
			if sub[1] != "" && !filepath.IsAbs(sub[1]) {
				ck.fail(f+"."+sub[0], "must be an absolute path")
			}
		}
	}
	c.validateCredentials(ck)
	vaults := map[string]bool{}
	for i, v := range c.Vaults {
		f := fmt.Sprintf("vaults[%d]", i)
		switch {
		case v == "" || HasControl(v) || strings.Contains(v, "/"):
			ck.fail(f, "must be a vault name with no slash")
		case vaults[v]:
			ck.fail(f, "declares a vault twice")
		}
		vaults[v] = true
	}
	c.validateThroughput(ck)
	if c.RunBudget < 0 {
		ck.fail("run_budget", "must not be negative")
	}
	if c.QuietPeriod < 0 {
		ck.fail("quiet_period", "must not be negative")
	}
	validateRegistrations(ck, c.Projects)
	return ck.err()
}

func (c *HostConfig) validateCredentials(ck *checker) {
	for _, r := range c.CredentialRefs() {
		if !ValidCredentialRef(r.Name) {
			// The reason never quotes the value: a value here is likely a
			// pasted credential.
			ck.fail("credentials."+r.Purpose, "must name a secret-store entry (upper case, digits and underscores), not hold a value")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Credentials.Trackers)) {
		if !validIdent(name) {
			ck.fail("credentials.trackers", "tracker names must be lower-case identifiers")
		}
	}
	if c.Credentials.GitHubApp.AppID < 0 {
		ck.fail("credentials.github_app.app_id", "must not be negative")
	}
	owners := map[string]bool{}
	for i, a := range c.Credentials.GitHubApps {
		f := fmt.Sprintf("credentials.github_apps[%d]", i)
		switch {
		case !ownerRe.MatchString(a.Owner):
			ck.fail(f+".owner", "must be a GitHub account name")
		case owners[strings.ToLower(a.Owner)]:
			ck.fail(f+".owner", "names an owner twice")
		}
		owners[strings.ToLower(a.Owner)] = true
		if a.AppID <= 0 {
			ck.fail(f+".app_id", "must be positive")
		}
		if a.PrivateKeySecret == "" {
			ck.fail(f+".private_key_secret", "must name the App's key in the secret store")
		}
	}
}

// CredentialRefs lists every platform credential that host config names, in
// a stable order. A server start reads each one from the store.
func (c *HostConfig) CredentialRefs() []CredentialRef {
	var refs []CredentialRef
	if n := c.Credentials.GitHubApp.PrivateKeySecret; n != "" {
		refs = append(refs, CredentialRef{Purpose: "github_app.private_key", Name: n})
	}
	for _, a := range c.Credentials.GitHubApps {
		refs = append(refs, CredentialRef{Purpose: "github_apps." + strings.ToLower(a.Owner) + ".private_key", Name: a.PrivateKeySecret})
	}
	if n := c.Credentials.MergifyTokenSecret; n != "" {
		refs = append(refs, CredentialRef{Purpose: "mergify_token", Name: n})
	}
	for _, name := range slices.Sorted(maps.Keys(c.Credentials.Trackers)) {
		refs = append(refs, CredentialRef{Purpose: "trackers." + name + ".token", Name: c.Credentials.Trackers[name].TokenSecret})
	}
	return refs
}
