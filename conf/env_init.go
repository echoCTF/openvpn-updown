package conf

import (
	"os"

	log "github.com/sirupsen/logrus"
)

type Environment struct {
	ID       string `json:"common_name"`
	Mode     string `json:"script_type"`
	LocalIP  string `json:"ifconfig_pool_remote_ip"`
	RemoteIP string `json:"untrusted_ip"`
}

func (env *Environment) Initialize() {
	// Create config structure
	//	env = &Environment{}
	env.Mode = os.Getenv("script_type")
	env.ID = os.Getenv("common_name")
	env.LocalIP = os.Getenv("ifconfig_pool_remote_ip")
	env.RemoteIP = os.Getenv("untrusted_ip")
	log.Debugf("%v", env)
}
