// Command currently-driver-snmp is the SNMP driver plugin: it reads UPSes
// through their network management cards, with the standard UPS-MIB
// (RFC 1628) or APC's PowerNet-MIB, over SNMPv1, v2c or v3.
//
//	ups:
//	  rack:
//	    driver: snmp
//	    host: 192.168.1.20
//	    community: public
package main

import "github.com/duskglow/currently/pkg/plugin"

func main() { plugin.ServeDriver(Driver{}) }
