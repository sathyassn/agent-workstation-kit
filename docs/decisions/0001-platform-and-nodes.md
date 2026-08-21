# Decision: Ubuntu first, multiple nodes

- **Status:** accepted
- **Decision:** Use supported Ubuntu LTS on multiple Ryzen AI Max+ 395-class mini PCs. Add macOS nodes later for Apple-platform development.
- **Reason:** workload isolation, incremental capacity, less browser/build contention, and reduced single-node dependence.
- **Not selected as baseline:** Omarchy/Quattro and NixOS. They remain experiments because the production baseline benefits from mainstream vendor support and lower desktop/driver complexity.
- **Revisit when:** the pilot burn-in, hardware support, or organization policy demonstrates a clear benefit.
