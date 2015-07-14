package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/snaury/copper"
)

const (
	// http://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.xhtml
	// Ports 5322-5342 are not currently assigned, however 5335 is known to be
	// used by mDNSResponder service, we currently use 5323.
	defaultPort       = "5323"
	defaultListenAddr = ":" + defaultPort
	defaultConfigFile = "/etc/copper-node.yaml"
)

func main() {
	configFile := flag.String("config", "", "config filename")
	flag.Parse()

	var config CopperNodeConfig
	if len(*configFile) == 0 && fileExists(defaultConfigFile) {
		*configFile = defaultConfigFile
	}
	if len(*configFile) != 0 {
		config = loadConfig(*configFile)
	}
	if len(config.Listen) == 0 {
		config.Listen = []ListenAddr{
			ListenAddr{
				Network:      "tcp",
				Address:      defaultListenAddr,
				AllowChanges: true,
			},
		}
	}

	server := copper.NewServer()
	defer server.Close()

	var hostports []string
	for _, listen := range config.Listen {
		if listen.Network == "" && len(listen.Address) > 0 {
			if listen.Address[0] == '/' || listen.Address[0] == '.' {
				listen.Network = "unix"
			} else {
				listen.Network = "tcp"
			}
		}
		if strings.HasPrefix(listen.Network, "tcp") {
			host, port, err := net.SplitHostPort(listen.Address)
			if err != nil {
				log.Fatalf("Invalid listen address %s/%s: %s", listen.Network, listen.Address, err)
			}
			if host != "" {
				hostports = append(hostports, listen.Address)
			} else {
				host, err = fullHostname()
				if err != nil {
					log.Fatalf("Failed to get current hostname: %s", err)
				}
				hostports = append(hostports, net.JoinHostPort(host, port))
			}
		} else if strings.HasPrefix(listen.Network, "unix") {
			os.Remove(listen.Address)
		}
		l, err := net.Listen(listen.Network, listen.Address)
		if err != nil {
			log.Fatalf("Failed to listen on %s/%s: %s", listen.Network, listen.Address, err)
		}
		server.AddListener(l, listen.AllowChanges)
		log.Printf("Listening on %s", listen.Address)
	}

	for dc, peers := range config.DCMap {
		islocal := peers.Contains(hostports...)
		distance := uint32(2)
		if islocal {
			distance = uint32(1)
		}
		for _, addr := range peers.OtherAddresses(hostports...) {
			err := server.AddPeer("tcp", addr, distance)
			if err != nil {
				log.Fatalf("Failed to add a peer %s/%s: %s", dc, addr, err)
			}
			log.Printf("Added remote peer %s (distance=%d)", addr, distance)
		}
	}

	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-signals
		log.Printf("Stopping due to signal: %s", sig)
		server.Close()
	}()

	log.Printf("Serving clients...")
	<-server.Done()
}
