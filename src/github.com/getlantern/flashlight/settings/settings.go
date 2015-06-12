// service for exchanging current user settings with UI
package settings

import (
	"net/http"
	"sync"

	"github.com/spf13/viper"

	//"github.com/getlantern/flashlight/analytics"
	"github.com/getlantern/flashlight/config"
	"github.com/getlantern/launcher"

	"github.com/getlantern/flashlight/ui"
	"github.com/getlantern/golog"
)

const (
	messageType = `Settings`
)

var (
	log           = golog.LoggerFor("flashlight.settings")
	service       *ui.Service
	cfgMutex      sync.RWMutex
	settingsMutex sync.RWMutex
	baseSettings  *Settings
	httpClient    *http.Client
)

type Settings struct {
	Version    string
	BuildDate  string
	AutoReport bool
	AutoLaunch bool
	ProxyAll   bool
}

func Configure(version, buildDate string) {

	cfgMutex.Lock()
	defer cfgMutex.Unlock()

	if service == nil {
		// base settings are always written
		baseSettings = &Settings{
			Version:    version,
			BuildDate:  buildDate,
			AutoReport: viper.GetBool("autoreport"),
			AutoLaunch: viper.GetBool("autolaunch"),
			ProxyAll:   viper.GetBool("client.proxyall"),
		}

		err := start(baseSettings)
		if err != nil {
			log.Errorf("Unable to register settings service: %q", err)
			return
		}
		go read()
	} else {
		if viper.GetBool("autolaunch") != baseSettings.AutoLaunch {
			// autolaunch setting modified on disk
			launcher.CreateLaunchFile(viper.GetBool("autolaunch"))
		}
		baseSettings = &Settings{
			Version:    version,
			BuildDate:  buildDate,
			AutoReport: viper.GetBool("autoreport"),
			AutoLaunch: viper.GetBool("autolaunch"),
			ProxyAll:   viper.GetBool("client.proxyall"),
		}
	}
}

// start the settings service
// that synchronizes Lantern's configuration
// with every UI client
func start(baseSettings *Settings) error {
	var err error

	helloFn := func(write func(interface{}) error) error {
		log.Debugf("Sending Lantern settings to new client")
		settingsMutex.RLock()
		defer settingsMutex.RUnlock()
		return write(baseSettings)
	}
	service, err = ui.Register(messageType, nil, helloFn)
	return err
}

func read() {
	log.Tracef("Reading settings messages!!")
	for msg := range service.In {
		log.Tracef("Read settings message!! %q", msg)
		settings := (msg).(map[string]interface{})
		transformed := map[string]interface{}{}
		transformed["autoreport"] = settings["autoReport"]
		transformed["autolaunch"] = settings["autoLaunch"]
		transformed["client.proxyall"] = settings["proxyAll"]
		// don't bother apply settings as lantern.yaml will be reload
		config.WriteParams(transformed)
		/*config.Update(func(updated *config.Config) error {

			if autoReport, ok := settings["autoReport"].(bool); ok {
				// turn on/off analaytics reporting
				if autoReport {
					analytics.StartService()
				} else {
					analytics.StopService()
				}
				baseSettings.AutoReport = autoReport
				*updated.AutoReport = autoReport
			} else if proxyAll, ok := settings["proxyAll"].(bool); ok {
				baseSettings.ProxyAll = proxyAll
				updated.Client.ProxyAll = proxyAll
			} else if autoLaunch, ok := settings["autoLaunch"].(bool); ok {
				launcher.CreateLaunchFile(autoLaunch)
				baseSettings.AutoLaunch = autoLaunch
				*updated.AutoLaunch = autoLaunch
			}
			return nil
		})*/
	}
}
