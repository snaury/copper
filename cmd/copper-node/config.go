package main

import (
	"io/ioutil"
	"log"
	"net"
	"strings"

	"gopkg.in/yaml.v2"
)

// ListenAddr is settings for a listening address
type ListenAddr struct {
	Type    string `yaml:"type"`
	Network string `yaml:"net"`
	Address string `yaml:"addr"`
	Changes bool   `yaml:"allow-changes"`
}

// DataCenterPeers is a list of datacenter peers
type DataCenterPeers []string

// Contains returns true if the list of peers contains current host/port
// The list of current hosts/ports must be lowercase
func (peers DataCenterPeers) Contains(hostports ...string) bool {
	for _, peer := range peers {
		index := strings.IndexByte(peer, ':')
		if index == -1 {
			peer = net.JoinHostPort(peer, defaultPort)
		}
		lpeer := strings.ToLower(peer)
		for _, hostport := range hostports {
			if lpeer == hostport {
				return true
			}
		}
	}
	return false
}

// OtherAddresses returns a slice with addresses that don't match current host/port
// The list of current hosts/ports must be lowercase
func (peers DataCenterPeers) OtherAddresses(hostports ...string) []string {
	var result []string
peerloop:
	for _, peer := range peers {
		index := strings.IndexByte(peer, ':')
		if index == -1 {
			peer = net.JoinHostPort(peer, defaultPort)
		}
		lpeer := strings.ToLower(peer)
		for _, hostport := range hostports {
			if lpeer == hostport {
				continue peerloop
			}
		}
		result = append(result, peer)
	}
	return result
}

// CopperNodeConfig is a parsed copper-node config
type CopperNodeConfig struct {
	Listen []ListenAddr `yaml:"listen"`

	DCMap map[string]DataCenterPeers `yaml:"dcmap"`
}

func loadConfig(filename string) (config CopperNodeConfig) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read %s: %#v", filename, err)
	}
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Fatalf("Failed to parse %s: %s", filename, err)
	}
	return
}
