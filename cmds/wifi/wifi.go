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
	"sync"
	"time"

	"github.com/d2g/dhcp4"
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
	if n, err := rand.Read([]byte{0}); err != nil || n != 1 {
		log.Fatalf("We're sorry, the random number generator is not up. Please file a ticket")
	}

	ifnames, err := netlink.LinkList()
	if err != nil {
		log.Fatalf("Can't get list of link names: %v", err)
	}

	var wg sync.WaitGroup
	done := make(chan error)
	for _, i := range ifnames {
		if i.Attrs().Name != *iface {
			continue
		}
		wg.Add(1)
		go func(ifname string) {
			defer wg.Done()
			iface, err := ifup(ifname)
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
		log.Fatalf("No interfaces match %v\n", ifName)
	}
	fmt.Printf("%d dhclient attempts were sent", nif)
}

func dhclient4(iface netlink.Link) error {
	const (
		timeout     = time.Duration(15) * time.Second // Default timeout of dhclient cmd
		numRenewals = -1                              // Default renewals of dhclient cmd
		retry       = -1                              // Default retry of dhclient cmd
	)

	client, err = dhclient.NewV4(iface, timeout, retry)
	if err != nil {
		return fmt.Errorf("error: %v", err)
	}
	var packet dhclient.Packet
	for i := 0; numRenewals < 0 || i < numRenewals+1; i++ {
		var success bool
		for i := 0; i < retry || retry < 0; i++ {
			if i > 0 {
				if needsRequest {
					debug("Resending DHCPv4 request...\n")
				} else {
					debug("Resending DHCPv4 renewal")
				}
			}

			if needsRequest {
				packet, err = client.Solicit()
			} else {
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

		if !success {
			return fmt.Errorf("%s: we didn't successfully get a DHCP lease", mac)
		}
		debug("IP Received: %v\n", packet.YIAddr().String())

		// We got here because we got a good packet.
		o := packet.ParseOptions()
		debug("Options: %v", o)

		netmask, ok := o[dhcp4.OptionSubnetMask]
		if ok {
			debug("OptionSubnetMask is %v\n", netmask)
		} else {
			// If they did not offer a subnet mask, we
			// choose the most restrictive option, namely,
			// our IP address.  This could happen on,
			// e.g., a point to point link.
			netmask = packet.YIAddr()
			debug("No OptionSubnetMask; default to %v\n", netmask)
		}
		dhclient.HandlePacket(iface, packet)
	}
	return nil
}
