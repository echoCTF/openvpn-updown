package main

import (
	"context"
	"database/sql"
	"flag"
	"os/exec"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/echoctf/openvpn-updown/conf"
	_ "github.com/go-sql-driver/mysql"
	log "github.com/sirupsen/logrus"
)

type EchoCTF struct {
	mc   *memcache.Client  // Hold our initialized Memcache connection
	db   *sql.DB           // Hold our initialized DB connection
	env  *conf.Environment // Hold our environment variables
	conf *conf.Config
}

type Target struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Network struct {
	codename string `json:"codename"`
}

const PFCTL string = "/sbin/pfctl"

// ParseFlags will create and parse the CLI flags
// and return the path to be used elsewhere
func ParseFlags() (string, error) {
	// String that contains the configured configuration path
	var configPath string

	// Set up a CLI flag called "-config" to allow users
	// to supply the configuration file
	flag.StringVar(&configPath, "config", "/etc/config.yml", "path to config file")

	// Actually parse the flags
	flag.Parse()

	// Validate the path first
	if err := conf.ValidateConfigPath(configPath); err != nil {
		return "", err
	}

	// Return the configuration path
	return configPath, nil
}

// Remove player from the pfctl tables
func (etsctf *EchoCTF) undoPlayerNetworks(common_name, ifconfig_pool_remote_ip string) {
	if !etsctf.conf.Pfctl.Enable {
		return
	}
	networks, _ := etsctf.selectNetworksForPlayer(etsctf.env.ID)
	for i := 0; i < len(networks); i++ {
		cmd := exec.Command(PFCTL, "-t", networks[i].codename+"_clients", "-T", "delete", etsctf.env.LocalIP)
		log.Debugf("Deleting %s from %s_clients", etsctf.env.ID, networks[i].codename)

		err := cmd.Run()

		if err != nil {
			log.Errorf("Failed to execute pfctl: %s", err.Error())
		}

		log.Debugf("Deleted %s from %s_clients", etsctf.env.ID, networks[i].codename)
	}

}

// Add users to the networks they are having access
func (etsctf *EchoCTF) doPlayerNetworks(common_name, ifconfig_pool_remote_ip string) {
	if !etsctf.conf.Pfctl.Enable {
		return
	}
	networks, _ := etsctf.selectNetworksForPlayer(common_name)
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
func (etsctf *EchoCTF) doVPN_LOGIN(common_name, ifconfig_pool_remote_ip, untrusted_ip string) error {
	stmtOut, err := etsctf.db.Prepare("CALL VPN_LOGIN(?,INET_ATON(?),INET_ATON(?))")
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
func (etsctf *EchoCTF) doVPN_LOGOUT(common_name, ifconfig_pool_remote_ip, untrusted_ip string) error {
	stmtOut, err := etsctf.db.Prepare("CALL VPN_LOGOUT(?,INET_ATON(?),INET_ATON(?))")
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
func (etsctf *EchoCTF) selectNetworksForPlayer(player_id string) ([]Network, error) {
	log.Printf("Getting networks for player")
	query := `SELECT codename FROM network WHERE (codename IS NOT NULL AND active=1) AND (public=1 or id IN (SELECT network_id FROM network_player WHERE player_id=?)) UNION SELECT LOWER(CONCAT(t2.name,'_',player_id)) AS codename FROM target_instance as t1 LEFT JOIN target as t2 on t1.target_id=t2.id WHERE player_id=?;`

	ctx, cancelfunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelfunc()

	stmt, err := etsctf.db.PrepareContext(ctx, query)
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

// Check if event is active
func (etsctf *EchoCTF) isEventActive() bool {
	it, err := etsctf.mc.Get("sysconfig:event_active")
	if err != nil || strings.TrimSpace(string(it.Value)) != "1" {
		return false
	}
	return true
}

// Check if user is online
func (etsctf *EchoCTF) isLogggedIn(common_name string) bool {
	if common_name == "" {
		common_name = etsctf.env.ID
	}

	_, err := etsctf.mc.Get("ovpn:" + common_name)
	if err == nil || err != memcache.ErrCacheMiss {
		log.Debugf("err: %v", err)
		log.Errorf("client %s already logged in", common_name)
		return true
	}
	return false
}

// Perform the client-connect
func (etsctf *EchoCTF) ClientConnect(common_name, ifconfig_pool_remote_ip, untrusted_ip string) {
	if !etsctf.isEventActive() {
		log.Fatalf("Not active sysconfig:event_active")
	}

	log.Infof("logging in client %s", common_name)

	if etsctf.isLogggedIn(common_name) {
		log.Fatalf("Already logged in exiting...")
	}

	etsctf.doVPN_LOGIN(common_name, ifconfig_pool_remote_ip, untrusted_ip)
	etsctf.doPlayerNetworks(common_name, ifconfig_pool_remote_ip)
	log.Infof("client %s logged in successfully", common_name)
}

// Perform the client-disconnect
func (etsctf *EchoCTF) ClientDisconnect(common_name, ifconfig_pool_remote_ip, untrusted_ip string) {
	if err := etsctf.doVPN_LOGOUT(common_name, ifconfig_pool_remote_ip, untrusted_ip); err != nil {
		log.Fatalf("Error: %s, exiting...", err.Error())
	}

	etsctf.undoPlayerNetworks(common_name, ifconfig_pool_remote_ip)
	log.Infof("client successfully disconnected cn:%s, local:%s, remote: %s", common_name, ifconfig_pool_remote_ip, untrusted_ip)
}

func main() {
	var etsctf = &EchoCTF{}

	log.SetLevel(log.InfoLevel)
	// parse the command line arguments
	cfgPath, err := ParseFlags()
	if err != nil {
		log.Fatal(err)
	}

	// parse the configuration file defined
	etsctf.conf, err = conf.NewConfig(cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	// Initial our Logging settings conf/conf.go
	etsctf.conf.InitLogger()

	etsctf.env = &conf.Environment{}
	etsctf.env.Initialize()
	etsctf.mc = memcache.New(etsctf.conf.Memcache.Host)
	etsctf.db, err = sql.Open("mysql", etsctf.conf.GetDSN())
	if err != nil {
		log.Errorf("Error connecting to mysql: %v", err)
	}
	defer etsctf.db.Close()
	etsctf.db.SetConnMaxLifetime(time.Second * 30)
	etsctf.db.SetMaxOpenConns(1)
	etsctf.db.SetMaxIdleConns(0)

	if etsctf.env.Mode == "client-connect" {
		etsctf.ClientConnect(etsctf.env.ID, etsctf.env.LocalIP, etsctf.env.RemoteIP)
	}

	if etsctf.env.Mode == "client-disconnect" {
		etsctf.ClientDisconnect(etsctf.env.ID, etsctf.env.LocalIP, etsctf.env.RemoteIP)
	}
	log.Exit(0)
}
