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
- **A video frame is read by the GPU where echod wrote it**, the same way: no upload. The canvas was
  too at first, and is uploaded now: see "The canvas is a copy" below.
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

- **The canvas is a copy.** kbase lends an imported user buffer to the GPU for the length of a job
  and unmaps it with `dma_unmap_page(..., DMA_BIDIRECTIONAL)`, which on arm64 invalidates the CPU's
  cache lines for those pages (`kbase_jd_user_buf_unmap`). Anything the CPU wrote to the buffer while
  the job ran and had not yet left its caches is dropped. For the page (GPU writes, CPU reads after)
  and a video frame (written before the call, which waits) that is exactly right. For echod's canvas
  it is a trap: the lift's ramp draws the last canvas again from another goroutine while the next
  frame is being drawn on it, and those strokes could be lost from the canvas itself. So the canvas
  is uploaded into its texture at each present (1.4 ms in `gpucheck`), as on the DRM boards, and the
  GPU never sees echod's memory. (A private copy for the GPU to read in place was no quicker: 2.3 ms
  against 2.1 in the same run.) `v1.6.34_sk5.1` still read the canvas in place; with Adaptive
  backlight off, as it is by default, nothing writes it during a job there.
- **Presents ahead.** Since the helper has its own copy, it answers a present as soon as the copy is
  taken (`OP_PRESENT` bit 1) and draws, copies and waits for the panel's refresh afterwards; the next
  request waits for that and carries the frame's error if it failed (`OP_SYNC` only waits). echod
  uses it whenever the lift is at rest (`gpu.Display.PresentAhead`, `Device.Settle` syncs). Before,
  `Present` kept echod's draw loop for 13-21 ms a frame where the cores' path, which turns the frame
  in the background, keeps it 1.5 ms; a turn screen that takes 25 ms to draw then missed its pace.
  Now it is 1.5 ms on both.
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

(These are from before the canvas became a copy: add about 2 ms of CPU a frame on the GPU side.)

Both paths wait for the panel's refresh in `FBIOPAN_DISPLAY`, which is why the time to the panel is one
refresh (16.7 ms) either way: the GPU's gain on this board is CPU time, not frame rate for a frame
echod has already drawn. (The 7 fps against 18-21 fps of
issue #251 was a shader effect moved to the GPU; echod's own drawing moved there later, for the
Rainbow: see "Effects".)

With echod running (the fork's, GPU path) and `screenbench` drawing 20 fps beside it for 75 s on each
path, the wake word's line stayed where it is at rest: `busy_pct` 8-9, `worst_ms` 2-8 on both.
echod's own frames (`screen frames` in its log): 13.6-16.8 ms mean on the GPU, 16.5-17.4 ms on the
cores.

### The turn screens, with echod's own renderer (`TestTurnBenchOnDevice`)

The daemon's renderer drawing its real turn screens onto the real panel (echod held, four cores up),
paced as the daemon paces them (66 ms a frame) and flat out. Device busy is all cores together, the
helper included, as a share of one core.

| screen | | cores | GPU, presents waited for (sk5.1) | GPU, presents ahead |
|---|---|---|---|---|
| bars, listening | paced | 15.0 fps, 48 % busy | 15.0 fps, 21 % | 15.0 fps, 20 % |
| bars, replying | paced | 15.0 fps, 48 % | 15.0 fps, 33 % | 15.0 fps, 32 % |
| Rainbow, listening | paced | 15.0 fps, 86 % | 15.0 fps, 80 % | 15.0 fps, 77 % |
| Rainbow, replying | paced | 14.9 fps, 109 % | **14.5 fps**, 93 % | 15.0 fps, 95 % |
| clock page | flat out | 58.0 fps | **30.7 fps** | 58.8 fps |
| bars, replying | flat out | 58.3 fps | **29.5 fps** | 58.5 fps |
| Rainbow, replying | flat out | 48.2 fps | **25.6 fps** | 47.7 fps |

So with presents waited for, the GPU path cost frame rate wherever drawing a frame takes more than
the rest of a refresh; with presents ahead it draws as fast as the cores' path and leaves between a
tenth (Rainbow, whose own drawing is the load) and more than half (the bars) of the processor time.
The microphone is not read in this bench, so the light and the bars are those of a quiet room.

## Effects: echod's own drawing on the GPU (the Rainbow)

With only the canvas's turn, lift, tint and video on the GPU, the Rainbow turn screen stayed most of
a core (77-95 %, above): its drawing is the load. So the helper now draws **effects**: frames through
fragment shaders of echod's own (`OP_FX_PROGRAM`, `OP_FX_TEXTURE`, `OP_FX_PRESENT`, `OP_FX_PASS` in
`tools/gpu/techo5-gpu.c`; `gpu.Program`, `gpu.Texture`, `gpu.Pass`, `screen.PresentEffect`). The
helper knows nothing of the Rainbow; the shaders, their pictures and numbers live with the screen's
code (`echod/internal/lib/rainbow/*.glsl`, `gpu.go`, `ongpu`). The page alone is drawn into the
canvas, and while it is kept (the Rainbow keeps it, redrawn once a second) it is neither copied back
nor uploaded again.

The T720 MP2 at 299 MHz does about 2 G scalar operations a second: one more cheap operation at every
pixel of the panel is ~0.25 ms. Working the light out at every pixel took 26 ms of a 40 ms frame, so
the GPU does what the cores do: the light and the corners at half size into a texture (a pass, 4 ms),
the card's picture into another only when it changes, and a frame program (8 ms) that lays them over
the page. A frame is 19.7 ms of the GPU (median, `rainbowcheck`, canvas copied every frame), of which
6.5 ms is the copy in and the copy to the panel. fp16 (`mediump`) made no difference.

The entry (the first 1.4 s of a turn) stays on the cores, and so does a frame under the update's
dialog or the camera's or the room's dots. A frame the GPU does not take is drawn on the cores after
all (`PaintGPU`) and shown the usual way; a shader the compiler refuses is not offered again.

Correctness:

- `rainbowcheck` (a turn drawn both ways from the same moments, the GPU's read back from the stand-in
  panel), on the Show and with llvmpipe alike: mean difference 0.62 of 255 a channel, 0.057 % of
  pixels off by more than 24, none in the listening, thinking and replying frames it saves. What
  differs: the line along the edge sampled per pixel, the words a pixel apart in frames where the
  card's spring rounds differently.
- `TestFinishGPU`: a turn given to the GPU and drawn on the cores after all is the cores' picture byte
  for byte, every frame after the entry.

Cost (`TestTurnBenchOnDevice`, echod held, 8 s each, device busy as a share of one core):

| | cores | GPU, canvas only (sk5.2) | GPU, effects |
|---|---|---|---|
| Rainbow, listening, paced 66 ms | 90 % | 77 % | **15 %** |
| Rainbow, replying, paced 66 ms | 103 % | 95 % | **23 %** |
| Rainbow, replying, flat out | 51.6 fps | 47.7 fps | 28.6 fps |

Flat out the effect frames come at half the panel's rate: a frame (19 ms) is longer than a refresh,
so each waits for the second vblank. The daemon paces the Rainbow at 15 fps.

### The Glow clock style, moving by itself

The Glow's colors (four soft blobs on Lissajous paths 89-211 s long) are drawn by the cores once a
second, as every clock is, and move up to 25 pixels a second on the Show 5: the panel showed a jump a
second. Now, on the GPU, the frame is an effect that **moves by itself**: the helper draws it again
every 66 ms until the next request (`OP_FX_PRESENT` a[4], a[5]; programs get `since`), with a pass
that works out how far the colors have changed since the page was drawn, at the field's own 121x61
points, added to the page. echod still draws once a second and does not wake in between.

- `TestGlowOnGPU` (a helper beside the running daemon): over 1.6 s the colors moved a mean 1.33 of
  255, 15 at most; the GPU's frame is the cores' colors at the same moment to a mean 0.22, 2 at most.
- On the unit (`v1.6.34_sk5.4`, the user's own Glow clock): panel captures 150 ms apart all differ;
  the helper takes 5 % of one core (99 ticks in 20 s).
- A Glow page costs the cores 27 ms (`BenchmarkGlowFrame`, Show 5; the classic face 6 ms), under 3 %
  of a core at its one frame a second. echod at rest measured 30 % of a core in the same 20 s, the
  wake word's 6-9 % among it: the rest is not the clock, and is still to be looked at.

### The GPU's clock

`/proc/gpufreq/gpufreq_opp_dump` on the vendor kernel: 520, 416 and 299 MHz, **all at 1.15 V**. Its
DVFS follows a utilization Arm's own user-space driver reports (`g_last_gpu_dvs_result = 127`, "not
enabled"), so with Mesa the GPU sat at 299 MHz. Writing a table frequency to
`/proc/gpufreq/gpufreq_opp_freq` keeps it there; `mt_gpufreq_target` (mtk_gpufreq.c) still applies
the thermal and power limits over a kept frequency. The helper now keeps the highest while it runs
and puts back the lowest when it exits (`TECHO5_GPU_CLOCK=off` leaves it alone). The Rainbow's frame
beside the running daemon: 21.7 ms at 299 MHz, 13.7 at 520. On the unit with the Glow moving: the
thermal zones at 43-50 °C after ten minutes, `g_limited_max_id = 0` (no limit applied).

A mainline kernel instead was looked at and set aside: no MT8163 support upstream (a community
fork stopped in 2020 at serial and simplefb), no MT7668 Wi-Fi driver upstream (only its Bluetooth),
and the image leans on the vendor kernel's Wi-Fi and Bluetooth modules, sound card, accdet, light
sensor and keypad.

### The weather's sky and a song's words, as items

Items (`OP_FX_ITEMS`, `gpu.Items`): quads drawn over a frame, each placed by its program's vertex half
from twelve numbers and `since`, colored by its fragment half. Up to four sets of 4096 a frame.

- The weather's sky: on the cores the whole page is drawn again every 83 ms (25 ms a frame with rain),
  and rain falling 300-600 pixels a second jumps 25-50 a frame. Now the page is drawn once a second
  and the sky is items the GPU moves at 30 fps, from weatherfx.go's own numbers for the next 2.5 s:
  streaks, hail, wind, flakes, fog's bands, the drops on the glass as lenses (trails, beads, mist) and
  lightning. `TestSkyOnGPU` on the Show, against the cores' sky at the moment most like it: rain 0.12
  of 255 (the sky itself changes the page by 0.39), snow 0.07/0.62, storm 0.46/17.4, fog 0.01/2.7;
  the helper 7-9 % of one core for any of them.
- A song's words: each line a picture of its own in one texture; the next line glides in over 360 ms,
  120 ms ahead of its time (`TestLyricsOnGPU`: before and after the words are the cores' to 2 of 255,
  classic and modern pages).

### `v1.6.34_sk5.5`

From 87d91120 (sha256 `be994a4e…`) into slot b (slot a keeps `sk5.4`). `techo5-gpu: GPU clock kept at
520 MHz (lowest 299)` at start, drew on the GPU at 17.5 s, wake word `busy_pct` 8-9, committed good.
The helper 5 % of a core with the Glow moving (150 ticks in 30 s), echod 27 %.

### When it goes wrong (all on the device)

| what | what happened |
|---|---|
| helper killed (`kill -9`) while echod draws on it | `gpu: helper dropped ... broken pipe`, the cores draw the next frame (within 2 s in the log); GPU retaken after 1 min |
| helper frozen (`kill -STOP`), as a hung GPU stack looks to echod | `gpu: no reply in time` after 3 s, helper killed, the cores draw |
| a job the GPU cannot finish (`tools/gpu/kbase/gles-hang.c`, a shader looping for seconds) | kbase hard-stops it after 5 s (`JS: Job Hard-Stopped`), Mesa logs `job 2 (atom 2) failed with event 0x4`, the call returns, the next frame draws correctly in 0.9 ms |
| the process killed in the middle of such a job | the next process renders at once; no kernel message |

| the same job from **another process while echod draws on the GPU** (the installed image) | the GPU is busy with it, echod's helper gets no reply for 3 s: `gpu: no reply in time`, the helper is killed (kbase resets the GPU: `Reset complete`), the cores draw. The picture stood still for 4.6 s (`screen frames ... worst_ms=4636`); GPU retaken 60 s later |

echod's 3 s limit is shorter than kbase's 5 s, so a stuck GPU costs three to five seconds of frozen
picture before the cores take over. The daemon stayed up through all of these, and the wake word's
line never moved.

## The image in the spare slot (2026-11-10)

`tools/linux/gpu-image.sh --version v1.6.34_sk5.1` → `techo5-rootfs-gpu-v1.6.34_sk5.1.tar.gz` (61 MB,
3 min on 16 threads with Mesa built from nothing). Against the v1.6.34 release it is made on, its
file list differs by: the daemon, `techo5-gpu`, Mesa's five libraries and `drirc.d`, the packages
libdrm, libpciaccess, hwdata-pci and expat, Mesa's licenses, and the apk database and release files.
Owners and modes of everything else are as in the base.

Installed with `slotctl install` into slot b and rebooted:

- up and answering SSH 20 s after the kernel started; `gpu up renderer="Mali-T720 MC2 (Panfrost)"` and
  `screen: drawn by the GPU` at 17.6 s, before the splash was done;
- slot b went from `trial 2` to `good` after its five minutes; slot a (v1.6.34, the cores) is still
  there and good: `slotctl switch a` and a reboot go back;
- at rest: `screen frames count=31 mean_ms=12-15 worst_ms=20-22 drawn_by=gpu`, `wake frames ...
  busy_pct=8`;
- all the tools pass against the image's own Mesa, beside the running daemon;
- the helper is 27 MB resident (43 MB virtual); the shader cache on `/data` is 2.4 MB;
- 20 fps of full-screen change drawn by a second process on the GPU beside the daemon: 17 % of one
  core on top of the 26 % the device is busy with at rest.

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

### `v1.6.34_sk5.2` (the canvas as a copy, presents ahead)

Installed into slot a the same evening (slot b keeps `sk5.1`; the release's own
`rootfs-v1.6.34.tar.gz` is in `/data/techo5-linux` on the unit for `slotctl install`, should the
cores' image be wanted back). Booted, drew on the GPU at 17.5 s, committed good after five minutes.

- echod's own frames: `screen frames ... mean_ms=1.5-1.8 worst_ms=2.1-2.9 drawn_by=gpu`, where `sk5.1`
  had 12-15 and 20-22: the draw loop is held for the copy only;
- `gpucheck` against the installed helper: all exact or within 1/255 as before; a present ahead
  frees the canvas after 1.3 ms, and a canvas painted over right after it does not show in the frame;
- 3600 frames at 60 fps from a second process: 61 fps flat out, 2.0 ms of its CPU a frame;
- the job that cannot finish, from another process: fell back after 3 s (picture still for 4.1 s),
  GPU retaken a minute later; wake word `busy_pct` 8-9 throughout.

### `v1.6.34_sk5.3` (effects: the Rainbow drawn by the GPU)

`tools/linux/gpu-image.sh --version v1.6.34_sk5.3` from f4b47afa (61 MB, sha256 `1c4aa13e…`),
installed into slot b (which had `sk5.1`; slot a keeps `sk5.2`). Booted, SSH back 22 s after the
reboot, `gpu up` and `screen: drawn by the GPU` at 17.5 s, wake word `busy_pct=8`.

- `rainbowcheck` against the image's own helper and Mesa, beside the running daemon: the same picture
  as before (0.62 of 255, 0.057 % off by more than 24); 13.3 ms a frame (median) on the fresh boot;
- `TestTurnBenchOnDevice` against the image's helper (echod held, then brought back): Rainbow
  listening 14 % of one core against 90 % on the cores, replying 22 % against 105 %, every paced frame
  through the GPU;
- the device's load average sits at 5.5 with the processor 100 % idle: Amazon's kernel threads
  (`GCPU`, `display_esd_che`, `dynamic_boost`, ...) wait in D state and count. Nothing of ours.

This Show is set to the classic turn screen (no `turn_style` in its state), so the daemon itself
draws no Rainbow until it is chosen (Settings, or Home Assistant's turn style select).

### `v1.6.34_sk5.4` (the Glow moving smoothly)

From 8953cdc2 (sha256 `f196aee2…`), into slot a (which had `sk5.2`; slot b keeps `sk5.3`). Booted,
drew on the GPU at 17.3 s, wake word `busy_pct` 6-9, committed good after five minutes.

### Soak

`screenbench -fps 60 -seconds 300` on the real panel from a second process, beside the running daemon
(itself on the GPU): 18 000 frames flat out, then 18 000 paced at 60 fps, every pixel new.

- no GL or kernel error, no warning in the daemon's log; its own frames stayed `drawn_by=gpu`;
- 62 fps flat out (15.97 ms mean, 33.4 worst); at 60 fps 45 % of one core over the device at rest, 2.0 ms
  of the drawing process's CPU a frame;
- the helper's resident memory: 20.6 MB for the first 18 000 frames, one step to 27.6 MB, flat after;
  the device's available memory moved 745 → 741 MB over the ten minutes;
- wake word during it: `busy_pct` 9, `worst_ms` 3-12.

## The fork and its pipeline

`github.com/bjakra/sauerkraut5`, default branch `gpu-kbase` (TECHO5's `main` plus this work).
`.github/workflows/gpu-image.yml` runs `tools/linux/gpu-image.sh` there. Its first run built the image
in 8.5 minutes (Mesa from nothing on the runner) and passed its checks. Against the image built on the
PC, the helper, `libgallium`, `libEGL` and `libdrm` in it are **byte for byte the same** (podman here,
docker there); the file lists differ only in the release line and in which name of each group of
hard-linked zoneinfo files the archive stores first.

A tag `sk5-vX.Y.Z.N` makes a release (attested): `sk5-v1.6.34.2` is the first, with
`techo5-rootfs-gpu-v1.6.34_sk5.2.tar.gz` and its SHA256SUMS.

Mesa upstream does not take backends for kernel drivers outside mainline (that is why panfork was a
fork), so the three patches are carried here rather than proposed there.

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
