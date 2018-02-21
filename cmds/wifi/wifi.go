// Copyright 2017 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"github.com/u-root/u-root/pkg/dhclient"
	"github.com/u-root/u-root/pkg/wpa/passphrase"
	"github.com/vishvananda/netlink"
)

const (
	cmd          = "wifi [options] essid [passphrase] [identity]"
	nopassphrase = `network={
		ssid="%s"
		proto=RSN
		key_mgmt=NONE
	}`
	eap = `network={
		ssid="%s"
		key_mgmt=WPA-EAP
		identity="%s"
		password="%s"
	}`
)

func init() {
	defUsage := flag.Usage
	flag.Usage = func() {
		os.Args[0] = cmd
		defUsage()
	}
}

func main() {
	var (
		iface = flag.String("i", "wlan0", "interface to use")
		essid string
		conf  []byte
	)

	flag.Parse()
	a := flag.Args()

	switch {
	case len(a) == 3:
		essid = a[0]
		conf = []byte(fmt.Sprintf(eap, essid, a[2], a[1]))
	case len(a) == 2:
		essid = a[0]
		pass := a[1]
		o, err := passphrase.Run(essid, pass)
		if err != nil {
			log.Fatalf("%v %v: %v", essid, pass, err)
		}
		conf = o
	case len(a) == 1:
		essid = a[0]
		conf = []byte(fmt.Sprintf(nopassphrase, essid))
	default:
		flag.Usage()
		os.Exit(1)
	}

	if err := ioutil.WriteFile("/tmp/wifi.conf", conf, 0444); err != nil {
		log.Fatalf("/tmp/wifi.conf: %v", err)
	}

	// There's no telling how long the supplicant will take, but on the other hand,
	// it's been almost instantaneous. But, further, it needs to keep running.
	go func() {
		if o, err := exec.Command("wpa_supplicant", "-i"+*iface, "-c/tmp/wifi.conf").CombinedOutput(); err != nil {
			log.Fatalf("wpa_supplicant: %v (%v)", o, err)
		}
	}()

	// Equivalent to
	// cmd := exec.Command("dhclient", "-ipv4=true", "-ipv6=false", "-verbose", *iface)

	// if we boot quickly enough, the random number generator
	// may not be ready, and the dhcp package panics in that case.
	if n, err := rand.Read([]byte{0}); err != nil || n != 1 {
		log.Fatalf("We're sorry, the random number generator is not up. Please file a ticket")
	}

	ifRE := regexp.MustCompilePOSIX(*iface)

	ifnames, err := netlink.LinkList()
	if err != nil {
		log.Fatalf("Can't get list of link names: %v", err)
	}

	var wg sync.WaitGroup
	done := make(chan error)
	for _, i := range ifnames {
		if !ifRE.MatchString(i.Attrs().Name) {
			continue
		}
		wg.Add(1)
		go func(ifname string) {
			defer wg.Done()
			iface, err := dhclient.IfUp(ifname)
			if err != nil {
				done <- err
				return
			}
			wg.Add(1)
			done <- dhclient4(iface)
			wg.Done()
		}(i.Attrs().Name)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait for all goroutines to finish.
	var nif int
	for err := range done {
		if err != nil {
			log.Print(err)
		}
		nif++
	}

	if nif == 0 {
		log.Fatalf("No interfaces match %v\n", *iface)
	}
	fmt.Printf("%d dhclient attempts were sent", nif)
}

func dhclient4(iface netlink.Link) error {
	const (
		timeout = time.Duration(15) * time.Second // Default timeout of dhclient cmd
		slop    = 10 * time.Second                // Default slop of dhclient cmd
		retry   = -1                              // Default retry of dhclient cmd
	)

	var (
		packet       dhclient.Packet
		mac          = iface.Attrs().HardwareAddr // For debuging purposes
		needsRequest = true
	)

	client, err := dhclient.NewV4(iface, timeout, retry)
	if err != nil {
		return fmt.Errorf("error: %v", err)
	}

	for {
		log.Printf("Start getting or renewing DHCPv4 lease\n")

		for i := 0; i > -1; i++ {
			if needsRequest {
				if i > 0 {
					log.Printf("Resending DHCPv4 request...\n")
				}
				packet, err = client.Solicit()
			} else {
				if i > 0 {
					log.Printf("Resending DHCPv4 renewal\n")
				}
				packet, err = client.Renew(packet)
			}
			if err != nil {
				if err0, ok := err.(net.Error); ok && err0.Timeout() {
					log.Printf("%s: timeout contacting DHCP server", mac)
				} else {
					log.Printf("%s: error: %v", mac, err)
				}
			} else {
				// Client needs renew after no matter what state it is now.
				needsRequest = false
				break
			}
		}

		if err := dhclient.HandlePacket(iface, packet); err != nil {
			return fmt.Errorf("error handling pakcet: %v", err)
		}
		if packet.Leases()[0].ValidLifetime == 0 {
			log.Printf("%v: server returned infinite lease.\n", iface.Attrs().Name)
			break
		}

		// We can not assume the server will give us any grace time. So
		// sleep for just a tiny bit less than the minimum.
		time.Sleep(timeout - slop)
	}
	return nil
}
