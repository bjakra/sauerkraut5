# sauerkraut5 notes: the Mali-T720 of the 1st gen Show 5 on Mesa

A running log of the work to draw TECHO5's screen on the GPU of the Echo Show 5 1st gen (`checkers`,
MT8163, Mali-T720 MP2) with Mesa's Panfrost and nothing of Arm's or Amazon's in user space. Newest
findings are appended to each section. Everything said to be measured was measured on the device.

## The approach, and why

**Panfrost stays in user space as it is; a new `pan_kmod` backend talks to the vendor kernel's own
Mali driver (kbase, `/dev/mali0`) instead of a DRM driver.** No kernel change, no module, no boot image.

The alternatives, and what ruled them out:

| Path | Verdict |
|---|---|
| Mainline kernel with the panfrost DRM driver | Not for this board: no MT7668 Wi-Fi driver in mainline (TECHO5's porting-plan.md, "Kernel: downstream 4.9, not mainline"). |
| Backport the panfrost DRM driver (and DRM itself: `CONFIG_DRM` is off) into the 4.9 kernel, as modules | Panfrost needs the DRM scheduler (4.16), shmem GEM helpers (5.2), sync objects (4.12) and the Mali LPAE page table format (5.x), and MediaTek's clock and power code for the GPU lives inside kbase's platform layer. A kernel project on the only unit there is, against a driver that already works. |
| Unmodified Mesa over a fake libdrm that translates panfrost's ioctls to kbase | No Mesa patch to carry, but interception (ioctl, mmap, device enumeration) instead of a seam Mesa already has, and no room for what kbase can do that panfrost's UAPI cannot say. |
| **A kbase backend for `pan_kmod`** | `pan_kmod` is Mesa's own abstraction over kernel drivers (panfrost, panthor). The job manager path of the gallium driver touches the kernel in a dozen places beyond it (sync objects, one submit ioctl), which became hooks. kbase keeps doing power, clocks, DVFS and job scheduling as it does for the blob. |

Prior art: icecream95's panfork (Mesa 23.0) ran Panfrost on kbase for the Mali-G610; its
`src/panfrost/base` showed what kbase needs from a caller (the upstream repository is gone; a mirror is at
`github.com/ROCKNIX/mesa-panfork`). Nothing of it is copied: Mesa has since grown `pan_kmod`, and the
backend here is written against that and against the kernel source.

## The device, as found (2026-11-10)

- TECHO5 v1.6.34 in slot a (good), slot b empty. Kernel `4.9.337-g8d928c5176cc` (arm64), userspace
  armv7 Alpine 3.24.2 (musl).
- kbase: `K:r26p0-01rel0(GPL)`, UK interface 11.11, the ioctl form (`KBASE_IOCTL_*`, type 0x80).
- GPU properties from `KBASE_IOCTL_GET_GPUPROPS`: GPU id `0x07201000` (T720 r1p0), 2 shader cores
  (`shader_present` 0x3), 1 core group, `mmu_features` 0x2821 (**33-bit GPU addresses**, 40-bit
  physical), `tiler_features` 0x809, `thread_max_threads` 0x100, `thread_features` 0x0a040400, job slots
  0-2, program counter 24 bits.
- GPU clock: 299 MHz under TECHO5 (`/proc/gpufreq`; steps 520/416/299 MHz, one voltage). Nothing
  raises it.
- kbase job timeouts (`/sys/class/misc/mali0/device/js_timeouts`): soft stop 100 ms, hard stop 5 s,
  GPU reset 5.5 s.
- Display: `mtkfb`, `/dev/fb0` 480x960 visible, 480x2880 virtual (three pages), 32 bpp.
- `CONFIG_DRM` is not set. The kernel has no `kcmp` either (Mesa says so once; harmless).

### Ways in

1. SSH over Wi-Fi.
2. **USB serial**: the gadget's ACM function is `/dev/ttyGS0` on the device, and busybox init keeps a
   root shell on it (`techo5-console`, in `/etc/inittab`); on the PC it is `/dev/ttyACM0`. Checked: a
   command typed there runs as root. It does not depend on the network or on echod. Open the port raw
   (no echo), or the PC's line discipline feeds the shell its own prompt back.
3. The boot path: a slot that never commits is rolled back after three boots; with no good slot the
   initramfs stays up as a rescue shell on the same serial port.

The kernel also has the RNDIS gadget function (`CONFIG_USB_CONFIGFS_RNDIS`), which TECHO5 can bind as
a network link (`USB_RNDIS` in device.conf); not set up here.

## kbase from our own code (`tools/gpu/kbase/kbase-probe.c`)

`kbase-probe` does the handshake, reads the properties, allocates in each zone and runs a chain of two
"write value" jobs with no library of Arm's. Result on the device: PASS, each job chain back in about
0.05 ms once the GPU is up.

What it established:

- A 32-bit process is a "compat" context: besides `SAME_VA` regions (GPU address = the CPU mapping's)
  it has the **custom VA zone**, where the kernel picks the GPU address (from 4 GB up) at allocation and
  mapping it on the CPU is optional (`mmap` at the offset the GPU address is). That is `pan_kmod`'s
  model, so the backend uses it. A 64-bit process has no such zone.
- The GPU runs job chains in memory above 4 GB (the T720 has 33 address bits).
- Executable regions are aligned to 16 MB (the program counter's range): first one at `0x101000000`.
- Memory mapped without `BASE_MEM_CACHED_CPU` needs no cache syncs; with it, `KBASE_IOCTL_MEM_SYNC`
  both ways.
- The cycle counter only runs for an atom that asks for it (`BASE_JD_REQ_PERMON`).
- Completion events are read from the device, 24 bytes each; `poll` says when.

## Mesa on kbase

Three commits on Mesa 26.1.6 (`tools/linux/patches/mesa`, once exported):

1. `pan/kmod, panfrost`: backend hooks for sync objects and job submission; libdrm and the DRM ioctl
   remain what happens without them.
2. `pan/kmod`: the kbase backend (`kbase_kmod.c`).
3. `loader, egl/surfaceless`: find `/dev/mali0` when no DRM device takes the display.

How the backend fills in for a DRM driver:

- **Sync objects** are a job's sequence number, signaled when the job's event has been read. No sync
  files, so `native_fence_fd` is off.
- **Order**: each atom is submitted with an order dependency on the one before, so job chains run as
  submitted. Panfrost's kernel driver orders jobs that share a BO; this orders all of them, which is
  correct and gives up running one frame's fragment job beside the next frame's tiler job.
- **BO waits**: a BO remembers the last job it was given to. Freeing a BO waits for that job, because
  nothing in kbase keeps a region alive under a running job.
- **Executable BOs** are pages of one 16 MB region, committed as they fill
  (`KBASE_IOCTL_MEM_COMMIT`): one region per shader BO would exhaust the address space after about
  250 of them.
- The context is the backend's own open of the device (via `/proc/self/fd`), since a kbase context is
  its file and can be set up once.

**First light (2026-11-10):** `tools/gpu/kbase/gles-smoke.c` (EGL surfaceless, a shaded triangle into a
texture, read back) on the device:

```
renderer Mali-T720 MC2 (Panfrost)
version  OpenGL ES 2.0 Mesa 26.1.6
  corner (background)     26  51  76 255      (cleared to 0.1 0.2 0.3)
  bottom left (red)      231  14  10 255
  bottom right (green)    14 231  10 255
  top (blue)               9  10 236 255
```

200 frames each at 256², 512² and 960²: no GL error, no kernel message, wake word `busy_pct` 7-9 as
before.

Frame time there is the read-back, not the drawing: 7.6 ms at 256², 29 ms at 512², 101 ms at 960², so
about 110 ns a pixel. `glReadPixels` copies out of write-combined GPU memory. A 480x960 frame would
take about 50 ms that way, so the screen must not be read back like this: see "Getting pixels to the
panel".

## Building

- No QEMU on the build machine, so Mesa is **cross-compiled**: an Alpine x86_64 container with clang
  and lld, and an armv7 sysroot filled by `apk --arch armv7 --root` (nothing in it runs). The build-time
  tools (`mesa_clc`, `vtn_bindgen2`, `panfrost_compile`) are built natively first, as
  `tools/linux/build-mesa.sh` does under emulation.
- clang ignores `#pragma GCC target("fpu=neon")` in `pan_afbc.c`, so the cross file turns NEON on for
  everything (`-mfpu=neon`). Every TECHO5 board has an ARMv8 core.
- At run time Mesa needs `libdrm.so.2` (still linked, never used on kbase), expat, zlib, zstd, libstdc++
  and libgcc; the device's image lacks only libdrm.

## Getting pixels to the panel

Open. Candidates, cheapest first:

1. CPU-cached, linear render target and a plain copy into `/dev/fb0`'s back page.
2. Render into memory the display reads: an ION buffer imported into kbase as a dma-buf and given to
   MediaTek's display manager as a layer (what Android's hwcomposer does), or `/dev/fb0`'s own pages if
   kbase can import them.

## Dead ends and traps

- `gitlab.com/panfork/mesa` now holds only a README pointing upstream.
- Running `/usr/local/bin/techo5 version` on the device starts the daemon's start-up path (it logged
  two lines and exited without disturbing the running one). Ask `slotctl status` for versions instead.
