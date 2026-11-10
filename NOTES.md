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

Three commits on Mesa 26.1.6, carried as `tools/linux/patches/mesa/000[1-3]-*.patch` and applied by
`tools/linux/build-mesa.sh --kbase`:

1. `pan/kmod, panfrost: let a backend own sync objects, job submission and imports`: optional backend
   hooks in `pan_kmod`; libdrm and the DRM ioctls remain what happens without them, so panfrost and
   panthor are untouched (not re-tested on a DRM board: the build keeps the patched Mesa apart for that
   reason).
2. `pan/kmod: add a backend for Arm's kbase kernel driver` (`kbase_kmod.c`, about 1100 lines).
3. `loader, egl/surfaceless: find a Mali behind kbase`: `/dev/mali0` when no DRM device takes the
   display.

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
- **Imports**: a dma-buf (`BASE_MEM_IMPORT_TYPE_UMM`), or, for a descriptor that is not one, the memory
  itself (`BASE_MEM_IMPORT_TYPE_USER_BUFFER`): the backend maps the fd and kbase pins those pages and
  maps them for the GPU while an atom that lists the region as an external resource runs, syncing the
  CPU's caches on the way in and out. Checked on the device with a memfd: the import succeeds, and so
  does drawing from and into one.

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
about 110 ns a pixel. `glReadPixels` copies out of write-combined GPU memory.

## Getting pixels to the panel

Copy speeds on the device for one 480x960 page (1.8 MB), best of ten:

| from → to | time |
|---|---|
| RAM → kbase memory (write-combined) | 1.0 ms |
| kbase memory (write-combined) → the same kind | 6.3 ms |
| kbase CPU-cached memory → write-combined | 1.0 ms |
| `KBASE_IOCTL_MEM_SYNC` of the page, either way | 0.17 ms |

So reading GPU memory is what is slow, writing is not. What was built on that:

- **The panel's pixels are drawn into a memfd** the helper gives GL as an EGL image
  (`EGL_EXT_image_dma_buf_import`; the backend imports it as a user buffer). The GPU writes it where it
  is, the CPU reads it at RAM speed, and the page goes to `/dev/fb0`'s back page with one copy
  (1.0-1.2 ms measured in the helper) and `FBIOPAN_DISPLAY`.
- **The canvas and a video frame are read by the GPU where echod wrote them**, the same way: no upload.
- `tools/gpu/kbase/gles-memfd.c` is that path alone: 30 frames, a 960x480 canvas turned onto a 480x960
  page, **0 pixels differing** from the turn done on the CPU, 3.3 ms a frame.

Not done, and why: scanning out a buffer the GPU drew into directly (an ION buffer given to MediaTek's
display manager as an overlay layer, which is what Android's hwcomposer does) would save the 1 ms copy.
It means taking the display path away from `mtkfb` while echod's CPU fallback depends on `mtkfb`
working exactly as it does now; mtkfb's own dma-buf export is compiled out (`#if 0` in `mtkfb.c`) and
the ION framebuffer heap is not built. One millisecond is not worth that risk on the only unit.

## The helper on the framebuffer (`tools/gpu/techo5-gpu.c`)

TECHO5's GPU helper drives DRM/KMS through GBM. It now has a second panel backend, used when there is
no DRM card: EGL surfaceless, the page as above, and `/dev/fb0`. The protocol with echod is unchanged,
so echod needed no board-specific code to use it (two small changes: it reads the page on show again
when it takes the panel back, and it logs its frames).

- **Shader variants.** The helper's canvas shader tested four uniforms for every pixel (rotated,
  clip, lift, blend). On the T720 that made the plain present 12 ms of GPU time; compiled per need
  (`#define LIFT/TONE/OVER`) it is 3-5 ms. This also speeds up the other boards.
- **Tint** has no display-pipeline block behind the framebuffer, so the color matrix is applied in the
  shader (and reported as a CTM); no curves.
- `OP_BLANK` is refused (`ENOSYS`): echod blanks the framebuffer itself, as before.
- `TECHO5_GPU_FB_TEST=WxH:file` makes a file stand in for the framebuffer, so the whole path runs
  beside a live echod, or on a PC with a software renderer.

### Correctness (`echod/cmd/gpucheck`, on the real panel, echod held)

| check | result |
|---|---|
| canvas turned onto the panel, 120 frames | 0 pixels differ |
| lift (gain 1.5, 4x4 dither) against the same arithmetic on the CPU | 0 pixels differ |
| color matrix (1, 0.5, 0.25) | within 1/255 |
| video frame with the canvas's controls blended over it | within 1/255 |

### Cost (`echod/cmd/screenbench`: echod's own `screen` package, real panel, echod held, 4 cores up, GPU at 299 MHz)

| | cores | GPU |
|---|---|---|
| every pixel new, Present to on the panel | 15.8 ms mean, 32.9 worst | 15.7 ms mean, 16.2 worst |
| the same at 20 fps: CPU busy, whole device | 54.7 % of one core | 14.3 % |
| CPU time of the drawing process a frame | 26.4 ms | 2.2 ms |
| a tenth of the screen new, at 20 fps | 57.7 %, 28.3 ms | 13.7 %, 1.7 ms |
| nothing new, at 20 fps | 56.5 %, 27.5 ms | 12.8 %, 1.2 ms |

Both paths wait for the panel's refresh in `FBIOPAN_DISPLAY`, which is why the time to the panel is one
refresh (16.7 ms) either way: the GPU's gain on this board is CPU time, a factor of twelve for the
drawing process, not frame rate for a frame echod has already drawn. (The 7 fps against 18-21 fps of
issue #251 was a shader effect moved to the GPU; nothing here moves echod's own drawing there yet.)

With echod running (the fork's, GPU path) and `screenbench` drawing 20 fps beside it for 75 s on each
path, the wake word's line stayed where it is at rest: `busy_pct` 8-9, `worst_ms` 2-8 on both.
echod's own frames (`screen frames` in its log): 13.6-16.8 ms mean on the GPU, 16.5-17.4 ms on the
cores.

### When it goes wrong (all on the device)

| what | what happened |
|---|---|
| helper killed (`kill -9`) while echod draws on it | `gpu: helper dropped ... broken pipe`, the cores draw the next frame (within 2 s in the log); GPU retaken after 1 min |
| helper frozen (`kill -STOP`), as a hung GPU stack looks to echod | `gpu: no reply in time` after 3 s, helper killed, the cores draw |
| a job the GPU cannot finish (`tools/gpu/kbase/gles-hang.c`, a shader looping for seconds) | kbase hard-stops it after 5 s (`JS: Job Hard-Stopped`), Mesa logs `job 2 (atom 2) failed with event 0x4`, the call returns, the next frame draws correctly in 0.9 ms |
| the process killed in the middle of such a job | the next process renders at once; no kernel message |

echod's 3 s limit is shorter than kbase's 5 s, so a stuck job costs three seconds of frozen picture
before the cores take over. The daemon stayed up through all of these.

## Building

- `tools/linux/build-mesa.sh --kbase --cross`: the patched Mesa, **cross-compiled** on x86_64 with no
  emulation (an Alpine container with clang and lld, an armv7 sysroot filled by `apk --arch armv7
  --root --no-scripts`, nothing in it run). The build-time tools (`mesa_clc`, `vtn_bindgen2`,
  `panfrost_compile`) are built natively first, as the emulated build does.
- clang ignores `#pragma GCC target("fpu=neon")` in `pan_afbc.c`, so the cross file turns NEON on for
  everything (`-mfpu=neon`). Every TECHO5 board has an ARMv8 core.
- `tools/gpu/build.sh --cross`: the helper, the same way (11 s).
- `tools/linux/gpu-rootfs.sh`: lays the daemon, the helper, Mesa and libdrm into a **released** root
  filesystem. Building one from nothing (`mkrootfs.sh`) needs QEMU for the packages' scripts and five
  other components built under emulation first; the release has them, and the delta is five paths.
  It refuses a base with a vendor tree.
- At run time Mesa needs `libdrm.so.2` (still linked, never used on kbase), expat, zlib, zstd, libstdc++
  and libgcc; a release lacks only libdrm.
- podman as docker needs fully qualified image names (`docker.io/amd64/alpine:3.24`); the scripts' new
  paths use them.

## Dead ends and traps

- `gitlab.com/panfork/mesa` now holds only a README pointing upstream.
- Running `/usr/local/bin/techo5 version` on the device starts the daemon's start-up path (it logged
  two lines and exited without disturbing the running one). Ask `slotctl status` for versions instead.
- `mount --bind` over `/usr/local/bin` (CONTRIBUTING.md's way to try a daemon) is busy for as long as
  the daemon runs from it: `umount -l`, then restart the daemon.
- `screenbench` beside a running echod on the **cores'** path leaves a stale page behind, because the
  cores write only rows that changed against their own shadow of each page. On the GPU path every
  frame is whole, so it is safe there.
- A Meson machine file takes single-quoted strings only, which do not fit inside the single-quoted
  container script of `build-mesa.sh`: the cross file is a file of its own.
