package config

import (
	"encoding/hex"
	"maps"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/duskglow/currently/internal/suggest"
	"github.com/duskglow/currently/outputs/snmp"
)

func (c *Config) validateSNMP(s *SNMP, checkAddr func(key, addr, example string), add func(path, format string, args ...any)) {
	checkAddr("listen.snmp.addr", s.Addr, "0.0.0.0:1161")
	if s.Community != "" && s.CommunityEnv != "" {
		add("listen.snmp.community", "set either listen.snmp.community or community_env, not both")
	}
	if s.CommunityEnv != "" && os.Getenv(s.CommunityEnv) == "" {
		add("listen.snmp.community_env", "listen.snmp.community_env names environment variable %s, but it is not set", s.CommunityEnv)
	}
	if s.UPS != "" {
		if _, ok := c.UPS[s.UPS]; !ok {
			names := c.UPSNames()
			add("listen.snmp.ups", "listen.snmp.ups: no UPS named %q%s (configured: %s)", s.UPS, suggest.DidYouMean(s.UPS, names), strings.Join(names, ", "))
		}
	}
	if s.EngineID != "" {
		if b, err := hex.DecodeString(strings.TrimPrefix(strings.ReplaceAll(s.EngineID, ":", ""), "0x")); err != nil || len(b) < 5 || len(b) > 32 {
			add("listen.snmp.engine_id", "listen.snmp.engine_id must be 5 to 32 bytes in hex, such as 80000000051a2b3c4d")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.Users)) {
		u := s.Users[name]
		key := "listen.snmp.users." + name
		auth, priv := snmp.NormalizeAuth(u.Auth), snmp.NormalizePrivacy(u.Privacy)
		if !slices.Contains(snmp.AuthProtocols, auth) {
			add(key+".auth", "SNMP user %s: auth %q is not one of %s%s", name, u.Auth,
				strings.Join(snmp.AuthProtocols, ", "), suggest.DidYouMean(auth, snmp.AuthProtocols))
		}
		switch {
		case priv == "des":
			add(key+".privacy", "SNMP user %s: DES is insecure and not supported; use privacy: aes", name)
		case !slices.Contains(snmp.PrivacyProtocols, priv):
			add(key+".privacy", "SNMP user %s: privacy %q is not one of %s", name, u.Privacy, strings.Join(snmp.PrivacyProtocols, ", "))
		case priv != "none" && auth == "none":
			add(key+".privacy", "SNMP user %s: privacy needs an auth protocol as well", name)
		}
		secret := func(what, literal, env string, needed bool) {
			switch {
			case !needed:
				if literal != "" || env != "" {
					add(key+"."+what+"_password", "SNMP user %s: %s_password is set but %s is none", name, what, what)
				}
			case literal != "" && env != "":
				add(key+"."+what+"_password", "SNMP user %s: set either %s_password or %s_password_env, not both", name, what, what)
			case literal == "" && env == "":
				add(key, "SNMP user %s: set %s_password or %s_password_env", name, what, what)
			case env != "" && os.Getenv(env) == "":
				add(key+"."+what+"_password_env", "SNMP user %s: %s_password_env names environment variable %s, but it is not set", name, what, env)
			case len(literal) > 0 && len(literal) < 8, env != "" && len(os.Getenv(env)) < 8:
				add(key+"."+what+"_password", "SNMP user %s: the %s password must have at least 8 characters", name, what)
			}
		}
		secret("auth", u.AuthPassword, u.AuthPasswordEnv, auth != "none")
		secret("privacy", u.PrivacyPassword, u.PrivacyPasswordEnv, priv != "none")
	}
	for i, t := range s.Traps {
		if _, _, err := net.SplitHostPort(TrapAddr(t.Target)); err != nil || t.Target == "" {
			add("listen.snmp.traps", "listen.snmp.traps[%d]: target %q is not a host or host:port", i, t.Target)
		}
	}
}

// TrapAddr adds the default trap port to a target without one.
func TrapAddr(target string) string {
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target
	}
	return net.JoinHostPort(strings.Trim(target, "[]"), "162")
}
