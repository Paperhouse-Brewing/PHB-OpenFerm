# PHB Fermenter (Starter Kit)

A small, readable, open-source **glycol fermentation controller** by **Paperhouse Brewing (PHB)**.

- **Arduino Mega**: real-time I/O (valves, pump, sensors).
- **Raspberry Pi Runtime (Go)**: control loops, state, web UI (SSE), serial comms.
- **Central Server (Go, optional)**: profile authoring/versioning + Brewfather integration (skeleton).

## Quick Start (Pi)
```bash
cd runtime-pi
go mod tidy
go run ./cmd/fermenter
# or build
go build -o ../bin/phb-fermenter ./cmd/fermenter
../bin/phb-fermenter
```

Open `http://<pi>:8080/`.

## PHB naming
- Binary: `phb-fermenter`
- Service: `phb-fermenter.service`
- Data dir: `/var/lib/phb`
- Metrics prefix: `phb_*`
- Profile schema: `phb.profile/v1`
