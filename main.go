package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/bradfitz/gomemcache/memcache"
	_ "github.com/go-sql-driver/mysql"
)

// Config struct for our configuration settings
type Config struct {
	Pfctl struct {
		Enable bool   `yaml:"enable"`
		Path   string `yaml:"path"`   // path for the pfctl binary default `/sbin/pfctl`
		Suffix string `yaml:"suffix"` // suffix to use for client tables default `_clients`
	} `yaml:"pfctl"`

	MySQL struct {
		Host       string `yaml:"host"`
		Port       string `yaml:"port"`
		Username   string `yaml:"username"`
		Password   string `yaml:"password"`
		Properties string `yaml:"properties"`
	} `yaml:"mysql"`

	Memcache struct {
		Host       string `yaml:"host"`
		Port       string `yaml:"port"`
		Username   string `yaml:"username"` // These are not used currently
		Password   string `yaml:"password"` // These are not used currently
		Properties string `yaml:"properties"`
	} `yaml:"memcache"`
}

type Target struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Network struct {
	codename string `json:"codename"`
}

const PFCTL string = "/sbin/pfctl"

// Get the environment variables
func GetEnvironmentVariables() (player_id, script_type, ifconfig_pool_remote_ip, untrusted_ip string) {
	script_type = os.Getenv("script_type")
	player_id = os.Getenv("common_name")
	ifconfig_pool_remote_ip = os.Getenv("ifconfig_pool_remote_ip")
	untrusted_ip = os.Getenv("untrusted_ip")
	return
}

// Remove player from the pfctl tables
func undoPlayerNetworks(db *sql.DB, common_name, ifconfig_pool_remote_ip string) {
	networks, _ := selectNetworksForPlayer(db, common_name)
	for i := 0; i < len(networks); i++ {
		cmd := exec.Command(PFCTL, "-t", networks[i].codename+"_clients", "-T", "delete", ifconfig_pool_remote_ip)
		log.Debugf("Deleting %s from %s_clients", common_name, networks[i].codename)

		err := cmd.Run()

		if err != nil {
			log.Fatal(err)
		}

		log.Debugf("Deleted %s from %s_clients", common_name, networks[i].codename)
	}

}

// Add users to the networks they are having access
func doPlayerNetworks(db *sql.DB, common_name, ifconfig_pool_remote_ip string) {
	networks, _ := selectNetworksForPlayer(db, common_name)
	for i := 0; i < len(networks); i++ {
		cmd := exec.Command(PFCTL, "-t", networks[i].codename+"_clients", "-T", "add", ifconfig_pool_remote_ip)
		log.Debugf("Adding %s to %s_clients", common_name, networks[i].codename)

		err := cmd.Run()

		if err != nil {
			log.Fatal(err)
		}
	}
}

// Call the VPN_LOGIN stored procedure
func doVPN_LOGIN(db *sql.DB, common_name, ifconfig_pool_remote_ip, untrusted_ip string) error {
	stmtOut, err := db.Prepare("CALL VPN_LOGIN(?,INET_ATON(?),INET_ATON(?))")
	if err != nil {
		log.Errorf("VPN_LOGIN failed to prepare: %v", err)
		return err
	}
	_, err = stmtOut.Exec(common_name, ifconfig_pool_remote_ip, untrusted_ip) // Insert tuples (i, i^2)
	if err != nil {
		log.Errorf("VPN_LOGIN failed to execute: %v", err)
		return err
	}
	defer stmtOut.Close()
	return nil
}

// Call the VPN_LOGOUT stored procedure
func doVPN_LOGOUT(db *sql.DB, common_name, ifconfig_pool_remote_ip, untrusted_ip string) error {
	stmtOut, err := db.Prepare("CALL VPN_LOGOUT(?,INET_ATON(?),INET_ATON(?))")
	if err != nil {
		log.Errorf("VPN_LOGOUT failed to prepare: %v", err)
		return err
	}
	_, err = stmtOut.Exec(common_name, ifconfig_pool_remote_ip, untrusted_ip) // Insert tuples (i, i^2)
	if err != nil {
		log.Errorf("VPN_LOGOUT failed to execute: %v", err)
		return err
	}
	defer stmtOut.Close()
	return nil
}

// Return the networks that the given player has access to
func selectNetworksForPlayer(db *sql.DB, player_id string) ([]Network, error) {
	log.Printf("Getting networks for player")
	query := `SELECT codename FROM network WHERE (codename IS NOT NULL AND active=1) AND (public=1 or id IN (SELECT network_id FROM network_player WHERE player_id=?)) UNION SELECT LOWER(CONCAT(t2.name,'_',player_id)) AS codename FROM target_instance as t1 LEFT JOIN target as t2 on t1.target_id=t2.id WHERE player_id=?;`

	ctx, cancelfunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelfunc()

	stmt, err := db.PrepareContext(ctx, query)
	if err != nil {
		log.Fatalf("Error %s when preparing SQL statement", err)
		return []Network{}, err
	}
	defer stmt.Close()

	rows, err := stmt.QueryContext(ctx, player_id, player_id)
	if err != nil {
		return []Network{}, err
	}
	defer rows.Close()

	var networks = []Network{}
	for rows.Next() {
		var ntwrk Network
		if err := rows.Scan(&ntwrk.codename); err != nil {
			return []Network{}, err
		}
		log.Debugf("Got network: %v", ntwrk.codename)

		networks = append(networks, ntwrk)
	}
	if err := rows.Err(); err != nil {
		return []Network{}, err
	}
	return networks, nil
}

func ClientDisconnect(mc *memcache.Client, db *sql.DB, common_name, ifconfig_pool_remote_ip, untrusted_ip string) {
	if err := doVPN_LOGOUT(db, common_name, ifconfig_pool_remote_ip, untrusted_ip); err != nil {
		log.Fatal("Exiting...")
	}
	undoPlayerNetworks(db, common_name, ifconfig_pool_remote_ip)
	log.Infof("disconnected successfully client cn:%s, local:%s, remote: %s", common_name, ifconfig_pool_remote_ip, untrusted_ip)
}

// Check if event is active
func isEventActive(mc *memcache.Client) bool {
	it, err := mc.Get("sysconfig:event_active")
	if err != nil || strings.TrimSpace(string(it.Value)) != "1" {
		return false
	}
	return true
}

func ClientConnect(mc *memcache.Client, db *sql.DB, common_name, ifconfig_pool_remote_ip, untrusted_ip string) {
	log.Printf("client connect %s", common_name)
	if !isEventActive(mc) {
		log.Fatalf("sysconfig:event_active")
	}

	USER_LOGGEDIN, err := mc.Get("ovpn:" + common_name)
	if err != nil && err != memcache.ErrCacheMiss && string(USER_LOGGEDIN.Value) != "" {
		log.Errorf("client %s already logged in", common_name)
		log.Errorf("USER_LOGGEDIN: %v", USER_LOGGEDIN)
		log.Errorf("err: %v", err)
		os.Exit(1)
	}
	log.Infof("logging in client %s", common_name)

	doVPN_LOGIN(db, common_name, ifconfig_pool_remote_ip, untrusted_ip)
	doPlayerNetworks(db, common_name, ifconfig_pool_remote_ip)
	log.Infof("client %s logged in successfully", common_name)
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

func main() {
	log.SetLevel(log.DebugLevel)
	var common_name, script_type, ifconfig_pool_remote_ip, untrusted_ip = GetEnvironmentVariables()
	log.Debugf("common_name:", common_name)
	log.Debugf("script_type:", script_type)
	log.Debugf("ifconfig_pool_remote_ip:", ifconfig_pool_remote_ip)
	log.Debugf("untrusted_ip:", untrusted_ip)

	mc := memcache.New("127.0.0.1:11211")
	db, err := sql.Open("mysql", "root:@/echoCTF")
	if err != nil {
		log.Errorf("Error connecting to mysql: %v", err)
	}
	defer db.Close()
	db.SetConnMaxLifetime(time.Second * 30)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)

	if script_type == "client-connect" {
		ClientConnect(mc, db, common_name, ifconfig_pool_remote_ip, untrusted_ip)
	}

	if script_type == "client-disconnect" {
		ClientDisconnect(mc, db, common_name, ifconfig_pool_remote_ip, untrusted_ip)
	}

	// -------ENV INIT END----------

	//mc.Set(&memcache.Item{Key: "foo", Value: []byte("my value")})
	it, err := mc.Get("sysconfig:event_active")
	if err != nil {
		panic(err)
	}

	fmt.Printf("key: %s, value: %s\n", it.Key, string(it.Value))
	//	db, err := sql.Open("mysql", "root:root@tcp(127.0.0.1)/echoCTF")

}
