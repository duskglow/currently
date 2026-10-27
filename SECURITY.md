# Security policy

Currently runs as root (or SYSTEM) on machines that matter, listens on the network, and can
shut computers down and switch UPS power. Please report:

* any way to run commands, change UPS variables, set forced shutdown (FSD) or shut a
  machine down without the credentials that should be required (a NUT user with the right
  role, the API token, an SNMP write community or v3 user, or `allow_control` for an output
  plugin);
* authentication bypasses in the NUT protocol server, the REST API or the SNMPv3 agent;
* crashes or hangs caused by crafted input: network requests (NUT protocol, SNMP, REST),
  plugin messages, or data from a malicious USB or serial device;
* passwords, tokens or communities leaking into logs, `currently config show`, captures or
  the API.

Report privately through GitHub Security Advisories on this repository
(Security → Report a vulnerability) rather than in a public issue. Reports go to the
maintainer, Russell Miller ([@duskglow](https://github.com/duskglow)), who fixes them,
requests a CVE where one is warranted, and publishes an advisory with the fixed release.

`currently.yaml` and `/etc/default/currently` hold passwords, API tokens and SNMP
communities. Treat them, and any logs taken with `log.level: debug`, as secrets.
