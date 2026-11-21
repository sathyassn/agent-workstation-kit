# Review checklist

[Documentation home](../README.md)

Use this for every review, with the [engineering standard](engineering.md).

- [ ] The PR has one purpose, and its description states the outcome and the evidence.
- [ ] Every new definition has one source; generated output is current.
- [ ] IDs and enums are typed; input is validated at each boundary it crosses.
- [ ] Errors are typed or sentinel and wrapped; no unit prints or exits.
- [ ] Tests cover the change: table-driven, golden or snapshot where output changed, fuzz for new parsers, integration through the engine for unit changes.
- [ ] Interrupt, resume and revert are tested for any change to the machine.
- [ ] macOS and Linux both have evidence, or the gap is stated.
- [ ] Docs and the unit's page (including its manual equivalent) are updated.
- [ ] The changelog notes catalog, registry, preset and breaking changes.
- [ ] No secret, personal name, host or private address appears.
- [ ] Core and safety PRs carry a cross-lineage review and stress evidence.
