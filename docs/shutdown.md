# Shutting down

Currently shuts this computer down when its UPS is about to run out, as NUT's
upsmon does. Without a `shutdown` section it only monitors.

```yaml
shutdown:
  when:
    low_battery: true        # the UPS's own low-battery signal (default)
    on_battery_for: 5m       # or after this long on battery
    runtime_below: 5m        # or when less runtime is left
  hooks:                     # run first, such as stopping VMs
    - /usr/local/bin/stop-vms
```

```sh
currently test shutdown   # the policy, how each UPS looks to it now, and every step (does nothing)
```

## Primary and secondaries

The computer the UPS is plugged into is its **primary**. Other computers
powered by the same UPS follow it as **secondaries**, either with NUT's
upsmon or with Currently's `nut` driver (which also follows NUT's own upsd):

```yaml
# on a secondary
ups:
  rack:
    driver: nut
    host: 192.168.1.10       # the primary (currently or NUT upsd)
    ups: rack
    username: mon            # a secondary user there, so the primary waits for us
    password_env: CURRENTLY_MON_PASSWORD
shutdown: {}                 # follow the primary's FSD, or low battery
```

On the primary, give each secondary a user:

```yaml
listen:
  nut: ":3493"
users:
  mon: {password_env: CURRENTLY_MON_PASSWORD, role: secondary}
```

When the primary decides to shut down, it:

1. sets FSD (forced shutdown) on the UPS, which every secondary sees;
2. waits up to `wait_for_secondaries` (15s) for the secondaries to log
   out as they shut down;
3. runs its `hooks`;
4. with `power_off_ups: true`, tells the UPS to cut power after
   `ups_off_delay` (60s) and to restore it when mains returns, so
   everything boots again by itself;
5. waits `final_delay` (5s) and runs the shutdown command
   (`systemctl poweroff`, `shutdown /s`, `shutdown -h now`), or
   `shutdown.command`.

A computer powered by several UPSes lists them in `shutdown.ups` and sets
`min_supplies`: it shuts down when fewer than that many are healthy.

## Testing it safely

- `currently test shutdown` prints the whole sequence and does nothing.
- `shutdown.dry_run: true` logs every step and does none of them.
- A simulated UPS (`simulator`, `replay`) never shuts the computer down:
  its outages only act on simulated UPSes, unless you set a custom
  `shutdown.command`. `currently cmd demo sim.power.fail` starts an outage.
- `CURRENTLY_NEVER_SHUTDOWN=1` in the environment blocks the operating
  system's shutdown command (a custom `shutdown.command` still runs, so
  point it at something harmless).
- Every decision and step appears in `currently watch`, the log, and
  `GET /api/v1/shutdown`.

!!! warning "Test `power_off_ups` on a spare UPS"
    `power_off_ups` sends real commands that cut the UPS's output. Try it
    with a lamp on a spare UPS before relying on it.
