package conf

import (
	"fmt"
	"io"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Config struct for our configuration settings
type Config struct {
	Loglevel string `yaml:"loglevel"`
	Logfile  string `yaml:"logfile"`

	Pfctl struct {
		Enable bool   `yaml:"enable"`
		Path   string `yaml:"path"`   // path for the pfctl binary default `/sbin/pfctl`
		Suffix string `yaml:"suffix"` // suffix to use for client tables default `_clients`
	} `yaml:"pfctl"`

	Mysql struct {
		Host       string `yaml:"host"` //protocol[(address)]
		Username   string `yaml:"username"`
		Password   string `yaml:"password"`
		Database   string `yaml:"database"`
		Properties string `yaml:"properties"`
	} `yaml:"mysql"`

	Memcache struct {
		Host       string `yaml:"host"`     // Host:port or /var/run/...
		Username   string `yaml:"username"` // These are not used currently
		Password   string `yaml:"password"` // These are not used currently
		Properties string `yaml:"properties"`
	} `yaml:"memcache"`
}

// NewConfig returns a new decoded Config struct
func NewConfig(configPath string) (*Config, error) {
	// Create config structure
	config := &Config{}

	// Open config file
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Init new YAML decode
	d := yaml.NewDecoder(file)

	// Start YAML decoding from file
	if err := d.Decode(&config); err != nil {
		return nil, err
	}

	return config, nil
}

// ValidateConfigPath just makes sure, that the path provided is a file,
// that can be read
func ValidateConfigPath(path string) error {
	s, err := os.Stat(path)
	if err != nil {
		return err
	}
	if s.IsDir() {
		return fmt.Errorf("'%s' is a directory, not a normal file", path)
	}
	return nil
}

func (conf *Config) SetLogfile() {
	if strings.TrimSpace(conf.Logfile) == "" {
		return
	}
	logFile, err := os.OpenFile(conf.Logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file %s for output: %s", conf.Logfile, err)
	}
	log.SetOutput(io.MultiWriter(logFile))
	log.RegisterExitHandler(func() {
		if logFile == nil {
			return
		}
		logFile.Close()
	})
}
func (c *Config) GetDSN() string {
	return fmt.Sprintf("%s:%s@%s/%s?%s", c.Mysql.Username, c.Mysql.Password, c.Mysql.Host, c.Mysql.Database, c.Mysql.Properties)
}

func (conf *Config) SetLoglevel() {
	lvl, err := log.ParseLevel(conf.Loglevel)
	if err != nil {
		log.Fatalf("Failed to parse loglevel [%s]: %s", conf.Loglevel, err.Error())
	}
	log.SetLevel(lvl)

}
func (conf *Config) InitLogger() {
	conf.SetLogfile()
	conf.SetLoglevel()
}
