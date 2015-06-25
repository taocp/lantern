package config

import (
	"compress/gzip"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"time"

	"github.com/getlantern/fronted"
	"github.com/getlantern/proxiedsites"
	"github.com/getlantern/yaml"

	"github.com/getlantern/flashlight/client"
)

// cloudConfig is the in memory representation of cloud.yaml
type cloudConfig struct {
	// To simplify, just use an ClientConfig object here.
	// Only those fields existed in cloud.yaml will take effect.
	Client       *client.ClientConfig
	ProxiedSites *proxiedsites.Config
	TrustedCAs   []*CA
}

const (
	CloudConfigPollInterval = 1 * time.Minute
	etag                    = "X-Lantern-Etag"
	ifNoneMatch             = "X-Lantern-If-None-Match"
)

var (
	cloudConfigChanged chan bool = make(chan bool)
)

func startCloudPoll() {
	go func() {
		for {
			time.Sleep(cloudPollSleepTime())
			cloudPoll()
		}
	}()
}

func cloudPoll() {
	newCfg := cloudConfig{}
	cfg := localCfg.Load().(*Config)
	b, err := fetchCloudConfig(cfg.CloudConfig)
	if err != nil {
		log.Errorf("Error fetch cloud config: %s", err)
		return
	}
	if b == nil {
		return
	}
	if err = newCfg.fromBytes(b); err != nil {
		log.Errorf("Error parse cloud config: %s", err)
		return
	}
	log.Debug("Applying cloud config")
	cloudCfg.Store(&newCfg)
	cloudConfigChanged <- true
}

func cloudPollSleepTime() time.Duration {
	return time.Duration((CloudConfigPollInterval.Nanoseconds() / 2) + rand.Int63n(CloudConfigPollInterval.Nanoseconds()))
}

func fetchCloudConfig(url string) ([]byte, error) {
	log.Debugf("Checking for cloud configuration at: %s", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Unable to construct request for cloud config at %s: %s", url, err)
	}
	if lastCloudConfigETag[url] != "" {
		// Don't bother fetching if unchanged
		req.Header.Set(ifNoneMatch, lastCloudConfigETag[url])
	}

	// make sure to close the connection after reading the Body
	// this prevents the occasional EOFs errors we're seeing with
	// successive requests
	req.Close = true

	resp, err := httpClient.Load().(*http.Client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Unable to fetch cloud config at %s: %s", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 304 {
		log.Debugf("Config unchanged in cloud at %s", url)
		return nil, nil
	} else if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Unexpected response status: %d", resp.StatusCode)
	}

	lastCloudConfigETag[url] = resp.Header.Get(etag)
	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Unable to open gzip reader: %s", err)
	}
	return ioutil.ReadAll(gzReader)
}

// fromBytes creates a new cloudConfig from given yaml.
func (updated *cloudConfig) fromBytes(updateBytes []byte) error {
	updated.Client = &client.ClientConfig{}
	updated.Client.FrontedServers = []*client.FrontedServerInfo{}
	updated.Client.ChainedServers = map[string]*client.ChainedServerInfo{}
	updated.Client.MasqueradeSets = map[string][]*fronted.Masquerade{}
	updated.ProxiedSites = &proxiedsites.Config{Delta: &proxiedsites.Delta{}}
	updated.TrustedCAs = []*CA{}
	err := yaml.Unmarshal(updateBytes, updated)
	if err != nil {
		return fmt.Errorf("Unable to unmarshal YAML for update: %s", err)
	}
	return nil
}
