# AI use policy

## How this project was made

Currently was written with [Claude Code](https://claude.com/claude-code), using Anthropic's
Claude Opus 5.5 model, under the direction of a human maintainer. The maintainer:

* set the goals;
* made the design decisions;
* tested it on real UPSes (CyberPower CP1500PFCLCDs, on Windows and Linux);
* reviewed the results.

Commits co-written with AI carry a `Co-Authored-By` trailer that says so.

## AI-assisted contributions are welcome

You may use AI tools to write code, tests or documentation for Currently. We care about the
quality of a contribution, not about which tools produced it. [CLAUDE.md](CLAUDE.md) gives
AI assistants the context they need, including how to add support for a new UPS.

## Your contribution is your responsibility

* **You are the author.** Whatever you submit, you are accountable for it: its correctness,
  its licensing, and its consequences. "The AI wrote it" is not an explanation for a bug, a
  hallucinated API, or a broken test. Low-quality contributions will be treated as
  low-quality contributions, whatever tool produced them.
* **Test it and review it yourself before you submit it.** Run the test suite. Exercise the
  change for real, against a real UPS or a capture of one where that makes sense. Read every
  line. If you cannot explain what a change does and why it is correct, it is not ready.
* **Check the facts.** Anything that claims something about UPS protocols, HID usages, SNMP
  OIDs, serial commands or NUT compatibility must be verified against the specification
  (the USB HID Power Device class, RFC 1628), the vendor's MIB or protocol documentation,
  NUT's protocol documentation, or a real UPS. Do not trust a model's description alone.
  A UPS monitor that acts on invented facts shuts computers down for no reason, or fails to
  shut them down before the battery runs out.

## Commit messages and pull requests

* **Descriptive and to the point.** Say what changed and why. Skip the boilerplate,
  filler and marketing tone.
* **Free of hallucinations.** Describe only what the change actually does, how you actually
  tested it, and what you actually observed. Do not claim tests pass that you did not run,
  cite issues that do not exist, or describe behaviour you did not verify.
* **Disclose substantial AI help.** Add a trailer such as
  `Co-Authored-By: <model or tool>` or `Assisted-by: <tool>` to the commit message. It is not
  a penalty. It helps reviewers know where to look harder.

## This project's own rules still apply

AI assistance changes nothing in [CONTRIBUTING.md](CONTRIBUTING.md). In particular:

* tests must never shut a computer down or send power commands to a real UPS (testing on
  your own hardware is up to you, but an AI tool should not do it unless you ask);
* nothing may silently guess what a UPS reports;
* do not copy code from NUT or other GPL projects;
* **never paste passwords, API tokens, SNMP communities or unredacted captures into an AI
  service** that you do not control. They protect someone's equipment.

Maintainers may ask how a change was produced and tested. They may decline contributions
that show signs of unreviewed generation, such as code that does not match its description,
tests that do not test anything, or confidently wrong explanations.
