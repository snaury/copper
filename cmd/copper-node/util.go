package main

import (
	"net"
	"os"
)

func fullHostname() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	cname, err := net.LookupCNAME(hostname)
	if err == nil {
		for len(cname) > 0 && cname[len(cname)-1] == '.' {
			cname = cname[:len(cname)-1]
		}
		if len(cname) > 0 {
			hostname = cname
		}
	}
	return hostname, nil
}

func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	if err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}
