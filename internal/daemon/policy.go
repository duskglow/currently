package daemon

import (
	"github.com/duskglow/currently/drivers/nutclient"
	"github.com/duskglow/currently/internal/config"
	"github.com/duskglow/currently/internal/policy"
)

// simulatedDrivers never trigger the built-in OS shutdown.
var simulatedDrivers = map[string]bool{"simulator": true, "replay": true}

// PolicyConfig turns the config's shutdown section into the engine's
// config, or nil when there is none (Currently then never shuts down).
func PolicyConfig(cfg *config.Config) *policy.Config {
	sd := cfg.Shutdown
	if sd == nil {
		return nil
	}
	pc := &policy.Config{
		MinSupplies:        sd.MinSupplies,
		LowBattery:         sd.When.LowBattery == nil || *sd.When.LowBattery,
		OnBatteryFor:       sd.When.OnBatteryFor,
		RuntimeBelow:       sd.When.RuntimeBelow,
		ChargeBelow:        sd.When.ChargeBelow,
		DeadTime:           sd.DeadTime,
		WaitForSecondaries: sd.WaitForSecondaries,
		FinalDelay:         sd.FinalDelay,
		Hooks:              sd.Hooks,
		HookTimeout:        sd.HookTimeout,
		Command:            sd.Command,
		PowerOffUPS:        sd.PowerOffUPS,
		UPSOffDelay:        sd.UPSOffDelay,
		DryRun:             sd.DryRun,
	}
	for _, name := range cfg.Supplies() {
		u := cfg.UPS[name]
		pc.Supplies = append(pc.Supplies, policy.Supply{
			Name:      name,
			Remote:    u.Driver == nutclient.DriverName,
			Simulated: simulatedDrivers[u.Driver],
		})
	}
	return pc
}
