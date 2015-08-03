package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	_ "net/http/pprof"

	"github.com/snaury/copper"
)

const (
	// http://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.xhtml
	// Ports 5322-5342 are not currently assigned, however 5335 is known to be
	// used by mDNSResponder service, we currently use 5323.
	defaultPort       = "5323"
	defaultConfigFile = "/etc/copper-node.yaml"
)

var defaultListenAddrs = []ListenAddr{
	ListenAddr{
		Network: "unix",
		Address: "/run/copper/copper.sock",
		Changes: true,
	},
	ListenAddr{
		Type:    "http",
		Network: "unix",
		Address: "/run/copper/copper.http",
	},
	ListenAddr{
		Network: "tcp",
		Address: "localhost:5323",
	},
	ListenAddr{
		Type:    "http",
		Network: "tcp",
		Address: "localhost:5380",
	},
}

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
		config.Listen = defaultListenAddrs
	}

	if config.CPU != 0 {
		log.Printf("Setting GOMAXPROCS to %d", config.CPU)
		runtime.GOMAXPROCS(config.CPU)
	}

	copper.SetErrorLog(log.New(os.Stderr, "", log.LstdFlags))
	copper.SetDebugLog(log.New(os.Stdout, "", log.LstdFlags))

	server := copper.NewServer()
	defer server.Close()

	var hostports []string
	for _, listen := range config.Listen {
		ishttp := false
		switch listen.Type {
		case "":
			// nothing
		case "http":
			ishttp = true
		default:
			log.Fatalf("Unsupported listen type %q", listen.Type)
		}
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
			hostport := listen.Address
			if host == "" {
				host, err = fullHostname()
				if err != nil {
					log.Fatalf("Failed to get current hostname: %s", err)
				}
				hostport = net.JoinHostPort(host, port)
			}
			hostports = append(hostports, strings.ToLower(hostport))
		} else if strings.HasPrefix(listen.Network, "unix") {
			os.Remove(listen.Address)
		}
		l, err := net.Listen(listen.Network, listen.Address)
		if err != nil {
			log.Fatalf("Failed to listen on %s/%s: %s", listen.Network, listen.Address, err)
		}
		if strings.HasPrefix(listen.Network, "unix") {
			err = os.Chmod(listen.Address, os.FileMode(0770))
			if err != nil {
				l.Close()
				os.Remove(listen.Address)
				log.Fatalf("Failed to chmod %s: %s", listen.Address, err)
			}
		}
		if ishttp {
			server.AddHTTPListener(l)
		} else {
			server.AddListener(l, listen.Changes)
		}
		log.Printf("Listening on %s", listen.Address)
	}

	for dc, peers := range config.DCMap {
		distance := uint32(1)
		if !peers.Contains(hostports...) {
			distance = uint32(2)
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
