// Command currently-driver-serial is the serial driver plugin: it reads
// UPSes on a serial port (or a USB serial adapter) with the Megatec/Q1 or
// APC Smart protocol.
//
//	ups:
//	  closet:
//	    driver: serial
//	    port: /dev/ttyUSB0       # COM3 on Windows
//	    protocol: auto           # megatec or apcsmart
package main

import "github.com/duskglow/currently/pkg/plugin"

func main() { plugin.ServeDriver(Driver{}) }
