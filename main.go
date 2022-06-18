package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/echoctf/openvpn-updown/conf"
	_ "github.com/go-sql-driver/mysql"
	log "github.com/sirupsen/logrus"
)

type EchoCTF struct {
	mc  *memcache.Client  // Hold our initialized Memcache connection
	db  *sql.DB           // Hold our initialized DB connection
	Env *conf.Environment // Hold our environment variables
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
	flag.StringVar(&configPath, "config", "./config.yml", "path to config file")

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

func (etsctf *EchoCTF) ClientDisconnect(common_name, ifconfig_pool_remote_ip, untrusted_ip string) {
	if err := etsctf.doVPN_LOGOUT(common_name, ifconfig_pool_remote_ip, untrusted_ip); err != nil {
		log.Fatal("Exiting...")
	}
	etsctf.undoPlayerNetworks(common_name, ifconfig_pool_remote_ip)
	log.Infof("disconnected successfully client cn:%s, local:%s, remote: %s", common_name, ifconfig_pool_remote_ip, untrusted_ip)
}

// Check if event is active
func (etsctf *EchoCTF) isEventActive() bool {
	it, err := etsctf.mc.Get("sysconfig:event_active")
	if err != nil || strings.TrimSpace(string(it.Value)) != "1" {
		return false
	}
	return true
}

func (etsctf *EchoCTF) ClientConnect(common_name, ifconfig_pool_remote_ip, untrusted_ip string) {
	log.Printf("client connect %s", common_name)
	if !etsctf.isEventActive() {
		log.Fatalf("sysconfig:event_active")
	}

	USER_LOGGEDIN, err := etsctf.mc.Get("ovpn:" + common_name)
	if err != nil && err != memcache.ErrCacheMiss && string(USER_LOGGEDIN.Value) != "" {
		log.Errorf("client %s already logged in", common_name)
		log.Errorf("USER_LOGGEDIN: %v", USER_LOGGEDIN)
		log.Fatalf("err: %v", err)
	}

	log.Infof("logging in client %s", common_name)

	etsctf.doVPN_LOGIN(common_name, ifconfig_pool_remote_ip, untrusted_ip)
	etsctf.doPlayerNetworks(common_name, ifconfig_pool_remote_ip)
	log.Infof("client %s logged in successfully", common_name)
}

func main() {
	// parse the command line arguments
	cfgPath, err := ParseFlags()
	if err != nil {
		log.Fatal(err)
	}

	// parse the configuration file defined
	cfg, err := conf.NewConfig(cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	lvl, _ := log.ParseLevel(cfg.Loglevel)
	log.SetLevel(lvl)

	var etsctf = &EchoCTF{}
	etsctf.Env = &conf.Environment{}
	etsctf.Env.Initialize()
	etsctf.mc = memcache.New(cfg.Memcache.Host)
	etsctf.db, err = sql.Open("mysql", cfg.Mysql.Username+":"+cfg.Mysql.Password+"@"+cfg.Mysql.Host+"/"+cfg.Mysql.Database)
	if err != nil {
		log.Errorf("Error connecting to mysql: %v", err)
	}
	defer etsctf.db.Close()
	etsctf.db.SetConnMaxLifetime(time.Second * 30)
	etsctf.db.SetMaxOpenConns(1)
	etsctf.db.SetMaxIdleConns(0)

	if etsctf.Env.Mode == "client-connect" {
		etsctf.ClientConnect(etsctf.Env.ID, etsctf.Env.LocalIP, etsctf.Env.RemoteIP)
	}

	if etsctf.Env.Mode == "client-disconnect" {
		etsctf.ClientDisconnect(etsctf.Env.ID, etsctf.Env.LocalIP, etsctf.Env.RemoteIP)
	}

	// -------ENV INIT END----------

	//mc.Set(&memcache.Item{Key: "foo", Value: []byte("my value")})
	it, err := etsctf.mc.Get("sysconfig:event_active")
	if err != nil {
		panic(err)
	}

	fmt.Printf("key: %s, value: %s\n", it.Key, string(it.Value))
	//	db, err := sql.Open("mysql", "root:root@tcp(127.0.0.1)/echoCTF")

}
