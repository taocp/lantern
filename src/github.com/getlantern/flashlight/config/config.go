package config

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"code.google.com/p/go-uuid/uuid"

	"github.com/getlantern/appdir"
	"github.com/getlantern/deepcopy"
	"github.com/getlantern/golog"
	"github.com/getlantern/proxiedsites"
	"github.com/getlantern/yamlconf"

	"github.com/getlantern/flashlight/client"
	"github.com/getlantern/flashlight/globals"
	"github.com/getlantern/flashlight/server"
	"github.com/getlantern/flashlight/statreporter"
)

var (
	log                 = golog.LoggerFor("flashlight.config")
	m                   *yamlconf.Manager
	lastCloudConfigETag = map[string]string{}
	httpClient          atomic.Value

	// localCfg stores a pointer to Config object, the in memory representation of lantern.yaml.
	// Although it uses Config object to avoid duplication of code,
	// those extra fields will always be overrode by cloud config or default.
	localCfg atomic.Value
	// localCfg stores a pointer to cloudConfig object, the in memory representation of cloud.yaml.
	cloudCfg atomic.Value
)

type Config struct {
	Version       int
	CloudConfig   string
	CloudConfigCA string
	Addr          string
	Role          string
	InstanceId    string
	CpuProfile    string
	MemProfile    string
	UIAddr        string // UI HTTP server address
	AutoReport    *bool  // Report anonymous usage to GA
	AutoLaunch    *bool  // Automatically launch Lantern on system startup
	Stats         *statreporter.Config
	Server        *server.ServerConfig
	Client        *client.ClientConfig
	ProxiedSites  *proxiedsites.Config // List of proxied site domains that get routed through Lantern rather than accessed directly
	TrustedCAs    []*CA
}

func Configure(c *http.Client) {
	httpClient.Store(c)
	// No-op if already started.
	m.StartPolling()
	startCloudPoll()
}

// CA represents a certificate authority
type CA struct {
	CommonName string
	Cert       string // PEM-encoded
}

// Init initializes the configuration system.
func Init() (*Config, error) {
	ccfg := emptyCloudConfig()
	ccfg.ApplyDefaults()
	cloudCfg.Store(ccfg)

	configPath, err := InConfigDir("lantern.yaml")
	if err != nil {
		return nil, err
	}
	m = &yamlconf.Manager{
		FilePath:         configPath,
		FilePollInterval: 1 * time.Second,
		ConfigServerAddr: *configaddr,
		EmptyConfig: func() yamlconf.Config {
			return &Config{}
		},
		OneTimeSetup: func(ycfg yamlconf.Config) error {
			cfg := ycfg.(*Config)
			return cfg.applyFlags()
		},
	}

	var cfg *Config
	initial, err := m.Init()
	if err != nil {
		return nil, err
	}
	cfg = initial.(*Config)
	localCfg.Store(cfg)
	cfg = mergedConfig()
	err = updateGlobals(cfg)
	if err != nil {
		return nil, err
	}
	return cfg, err
}

// Run runs the configuration system.
func Run(updateHandler func(updated *Config)) error {
	for {
		// wait for either local or cloud config changes
		// and merge them to form a complete config.
		select {
		case next := <-m.Next():
			localCfg.Store(next.(*Config))
		case <-cloudConfigChanged:
		}
		cfg := mergedConfig()

		if err := updateGlobals(cfg); err != nil {
			return err
		}
		updateHandler(cfg)
	}
}

func mergedConfig() *Config {
	merged := Config{}
	deepcopy.Copy(&merged, localCfg.Load().(*Config))
	cloud := cloudCfg.Load().(*cloudConfig)
	merged.Client.FrontedServers = cloud.Client.FrontedServers
	merged.Client.ChainedServers = cloud.Client.ChainedServers
	merged.Client.MasqueradeSets = cloud.Client.MasqueradeSets
	merged.ProxiedSites = cloud.ProxiedSites
	merged.TrustedCAs = cloud.TrustedCAs
	return &merged
}

func updateGlobals(cfg *Config) error {
	globals.InstanceId = cfg.InstanceId
	err := globals.SetTrustedCAs(cfg.TrustedCACerts())
	if err != nil {
		return fmt.Errorf("Unable to configure trusted CAs: %s", err)
	}
	return nil
}

// Update updates the configuration using the given mutator function.
func Update(mutate func(cfg *Config) error) error {
	return m.Update(func(ycfg yamlconf.Config) error {
		return mutate(ycfg.(*Config))
	})
}

// InConfigDir returns the path to the given filename inside of the configdir.
func InConfigDir(filename string) (string, error) {
	cdir := *configdir

	if cdir == "" {
		cdir = appdir.General("Lantern")
	}

	log.Debugf("Placing configuration in %v", cdir)
	if _, err := os.Stat(cdir); err != nil {
		if os.IsNotExist(err) {
			// Create config dir
			if err := os.MkdirAll(cdir, 0750); err != nil {
				return "", fmt.Errorf("Unable to create configdir at %s: %s", cdir, err)
			}
		}
	}

	return filepath.Join(cdir, filename), nil
}

// TrustedCACerts returns a slice of PEM-encoded certs for the trusted CAs
func (cfg *Config) TrustedCACerts() []string {
	certs := make([]string, 0, len(cfg.TrustedCAs))
	for _, ca := range cfg.TrustedCAs {
		certs = append(certs, ca.Cert)
	}
	return certs
}

// GetVersion implements the method from interface yamlconf.Config
func (cfg *Config) GetVersion() int {
	return cfg.Version
}

// SetVersion implements the method from interface yamlconf.Config
func (cfg *Config) SetVersion(version int) {
	cfg.Version = version
}

// ApplyDefaults implements the method from interface yamlconf.Config
//
// ApplyDefaults populates default values on a Config to make sure that we have
// a minimum viable config for running.  As new settings are added to
// flashlight, this function should be updated to provide sensible defaults for
// those settings.
func (cfg *Config) ApplyDefaults() {
	if cfg.Role == "" {
		cfg.Role = "client"
	}

	if cfg.Addr == "" {
		cfg.Addr = "localhost:8787"
	}

	if cfg.UIAddr == "" {
		cfg.UIAddr = "localhost:16823"
	}

	if cfg.CloudConfig == "" {
		cfg.CloudConfig = "https://config.getiantem.org/cloud.yaml.gz"
	}

	if cfg.InstanceId == "" {
		cfg.InstanceId = hex.EncodeToString(uuid.NodeID())
	}

	// Make sure we always have a stats config
	if cfg.Stats == nil {
		cfg.Stats = &statreporter.Config{}
	}

	if cfg.Stats.StatshubAddr == "" {
		cfg.Stats.StatshubAddr = *statshubAddr
	}

	if cfg.Client != nil && cfg.Role == "client" {
		cfg.applyClientDefaults()
	}
}

func (cfg *Config) applyClientDefaults() {
	if cfg.AutoReport == nil {
		cfg.AutoReport = new(bool)
		*cfg.AutoReport = true
	}

	if cfg.AutoLaunch == nil {
		cfg.AutoLaunch = new(bool)
		*cfg.AutoLaunch = false
	}
}

func (cfg *Config) IsDownstream() bool {
	return cfg.Role == "client"
}

func (cfg *Config) IsUpstream() bool {
	return !cfg.IsDownstream()
}
