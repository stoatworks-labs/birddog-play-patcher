# bdkvm — NDI KVM endpoint for the BirdDog PLAY

Reads the keyboard/mouse on the PLAY's USB-A host port and forwards them to the
NDI source the PLAY is displaying, as `<ndi_kvm>` metadata.

**Confirmed working on hardware** (BirdDog PLAY, firmware 1.0.30): attached to a
Windows NDI Screen Capture source over the PLAY's USB-A port and drove it from a
real keyboard and mouse, using the free NDI SDK only. The wire format is also
unit-tested against the byte-verified reference.

## Design

- Opens its **own** NDI receiver at `NDIlib_recv_bandwidth_metadata_only`, so it
  costs nothing on the wire and coexists with `PPApp`'s full-bandwidth receiver
  on the same source. `PPApp` is never touched.
- **Free NDI SDK only.** `NDIlib_recv_send_metadata` is not an Advanced entry
  point, and the send path works regardless of what the sender advertises in
  `<ndi_capabilities>`.
- `libndi` is **dlopen'd at runtime** (via purego), never linked — so no NDI
  code ships in this binary, and it uses whatever the device already has.
- Sends the **unshifted** X11 keysym and lets Shift travel as its own event, so
  the remote end applies its own layout.
- `EVIOCGRAB`s the devices so keystrokes don't also reach the PLAY's console.

## Build

```bash
tools/bdkvm/build.sh          # -> dist/bdkvm-linux-arm64
go test ./...                 # wire-format tests, run natively
```

purego needs cgo for `dlopen` on Linux and macOS has no aarch64-linux gcc, so
zig is the C compiler, pinned to `aarch64-linux-gnu.2.28` to match Debian 10.
Without the pin the binary targets a newer glibc and won't start on the device.

## Trying it without a PLAY

```bash
./bdkvm --dry-run -v      # prints <ndi_kvm .../> instead of sending; no NDI needed
```

## Known gaps

- Mouse is relative-to-absolute with a fixed virtual extent (`--ref-width/height`),
  so pointer tracking will drift from the remote cursor. A real implementation
  wants the remote screen size, which the sender does not advertise.
- Horizontal wheel (`REL_HWHEEL`) is ignored — no opcode was identified for it.
- Keypad keys map to their plain equivalents.
- No clipboard.
