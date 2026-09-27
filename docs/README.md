# Bzlhub documentation

The documentation is split by purpose so old design exploration is not
mistaken for current behavior.

## Current operator and contributor docs

- [README](../README.md): product overview, local setup, and supported
  configuration.
- [Roadmap](roadmap.md): current release blockers and ordered follow-up work.
- [Deployment](deployment/): self-hosting, Kubernetes parity, and reverse-proxy
  identity guidance.
- [Helm chart](../deploy/helm/bzlhub/README.md): chart-specific values and
  security constraints.

## Design and history

- [Original plan](plan.md): archived phased plan; unchecked boxes are not a
  status report.
- [Ideas](ideas.md): product exploration and rationale.
- [Research](research.md): technical investigations and ecosystem notes.
- [Feature plans](plans/): design records written at different points in the
  project. Treat statements about implementation as historical unless confirmed
  by code or the current roadmap.
- [Spikes](spikes/): experiment records.

When implementation and a historical plan disagree, implementation plus tests
are authoritative. Update the current roadmap when an active release blocker
changes.
