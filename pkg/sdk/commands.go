package sdk

// Command is an instant command a UPS accepts, such as test.battery.start.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// knownCommands holds descriptions for standard NUT instant commands.
var knownCommands = map[string]string{
	"beeper.enable":            "Enable the UPS beeper",
	"beeper.disable":           "Disable the UPS beeper",
	"beeper.mute":              "Silence the beeper until the next event",
	"test.battery.start":       "Start a battery test",
	"test.battery.start.quick": "Start a quick battery test",
	"test.battery.start.deep":  "Start a deep battery test",
	"test.battery.stop":        "Stop a running battery test",
	"test.panel.start":         "Start a front panel test",
	"calibrate.start":          "Start runtime calibration",
	"calibrate.stop":           "Stop runtime calibration",
	"load.off":                 "Turn off the load immediately",
	"load.on":                  "Turn on the load immediately",
	"load.off.delay":           "Turn off the load after ups.delay.shutdown",
	"shutdown.return":          "Turn off the load and restore it when power returns",
	"shutdown.stayoff":         "Turn off the load and keep it off",
	"shutdown.stop":            "Cancel a pending shutdown",
}

// NewCommand returns a Command with the standard description filled in when
// the name is a well-known NUT command.
func NewCommand(name string) Command {
	return Command{Name: name, Description: knownCommands[name]}
}
