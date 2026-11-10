// SPDX-License-Identifier: MIT
//
// techo5-gpu: the GPU for echod on the mainline 2nd gen Show 8 (Mali-G72) and 2nd gen Show 5
// (Mali-T720), and on the 1st gen Show 5's vendor kernel (Mali-T720 behind Arm's kbase driver), through
// Mesa's panfrost. One process owns the GPU and the panel, and echod asks it, over a SOCK_SEQPACKET
// socket on fd 3, for two things:
//
//   - the screen: echod draws its landscape canvas into shared memory as it always has; this uploads
//     it, draws it rotated onto the portrait panel, and shows it with a page flip at vblank (DRM/KMS
//     through GBM, so no tearing and no rotation on the cores). A video frame is drawn the same way,
//     with the canvas's controls blended over it. Where the kernel has no DRM (the 1st gen Show 5's)
//     the panel is the framebuffer device instead: see "the framebuffer's panel" below.
//   - the camera: a packed 10-bit Bayer frame is unpacked, demosaiced (bilinear, edges mirrored) and
//     put through echod's tone tables in a fragment shader, and read back as RGBA, or as YUYV for the
//     Show 8's JPEG encoder.
//
// The shaders are OpenGL ES 2.0, the most the Show 5's Mali-T720 offers. Should this process be
// missing or die, echod draws and develops on the cores as it did before.
//
// Messages are struct req one way and struct rep the other; buffers are memfds passed once, with
// SCM_RIGHTS, and mapped on both sides.

#define _GNU_SOURCE
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#include <errno.h>
#include <fcntl.h>
#include <gbm.h>
#include <linux/fb.h>
#include <poll.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>
#include <xf86drm.h>
#include <xf86drmMode.h>
#if defined(__aarch64__) || defined(__ARM_NEON)
#include <arm_neon.h>
#endif

enum {
	OP_HELLO = 1,
	OP_DISPLAY_OPEN = 2,	// a[0..2] canvas w, h, rotated; fd: the canvas (w*h*4)
	OP_PRESENT = 3,		// draw the canvas; a[0] bit 0: measure it first, the reply's a[0] then being its
				// brightest red, green or blue value plus 1; bit 1 (without bit 0): answer as soon
				// as the canvas has been copied, which frees echod to draw the next frame while
				// this one is drawn and waits for the panel. The request after it waits for the
				// frame to be on the panel, and is answered with the frame's error in its own
				// place if there was one. Only the framebuffer's panel answers early; elsewhere
				// the bit changes nothing.
	OP_PRESENT_FRAME = 4,	// a[0..1] frame w, h in panel orientation, a[2..5] overlay rect in canvas
				// coordinates (x0, y0, x1, y1), a[6] bit 0: the frame's bytes are B, G, R, A (the
				// framebuffer's order), else R, G, B, A; fd: the frame (w*h*4), passed when it changes
	OP_DEVELOP = 5,		// a[0..4] w, h, bpl, blue first, out; fds: raw, tables (3*2048 bytes), out,
				// passed when they change. out: 0 RGBA (w*h*4) and 1 YUYV (w*h*2) read back into
				// the out buffer; 3 YUV 4:2:0 planes (Y, Cb, Cr: w*h*3/2) read back likewise; 2
				// YUYV left in a linear buffer of the GPU's whose dmabuf the reply carries the
				// first time (a[0] 1 when it does, a[1] its stride), for a JPEG encoder to read in
				// place: on these SoCs the CPU reads GPU memory uncached, about 160 MB/s, so a
				// picture read back costs more than developing it.
	OP_BLANK = 6,		// a[0]: 1 off, 0 on
	OP_COLOR = 8,		// the display pipeline's own color blocks (CCORR and GAMMA, as the CRTC's CTM and
				// GAMMA_LUT): a[0] bit 0 a matrix is given, else the identity; bit 1 curves are given,
				// else none (the identity, should a curve have been set before); fd: struct color,
				// passed when it changes. The reply's a[0] is GAMMA_LUT_SIZE, a[1] bit 0 there is a
				// CTM, bit 1 a GAMMA_LUT. a[0] 0 with no CTM or LUT ever set costs nothing. On the
				// framebuffer's panel there are no such blocks: the matrix is applied where the
				// panel's pixels are drawn (and says so as a CTM), and there are no curves.
	OP_SYNC = 10,		// nothing of its own: answered once a frame presented early is on the panel
	// Effects: frames drawn through a fragment shader of echod's own, over the canvas (see "effects").
	OP_FX_PROGRAM = 11,	// a[0] the program's slot, a[1] bit 0: a pass's (OP_FX_PASS) rather than a
				// present's; fd: its source, text. The reply's text is the compiler's when it is
				// refused (-EINVAL).
	OP_FX_TEXTURE = 12,	// a[0] a texture's slot, a[1..2] its w, h, a[3] bit 0: read smoothly between its
				// texels, a[4..5] the rows changed, from and to (0, 0: all); fd: its pixels
				// (w*h*4, bytes R, G, B, A), passed when the buffer is new. Copied now.
	OP_FX_PRESENT = 13,	// draw the canvas through a program: a[0] bit 0: the canvas is as it was last
				// copied (no new copy), bit 1: answer early, as OP_PRESENT's; a[1] the program's
				// slot, a[2] how many of its parameters (vec4s) are given, a[3] the texture slots
				// it reads, a bit each; fd: the parameters (floats), passed when the buffer is new
	OP_FX_PASS = 14,	// draw a pass's program into a texture now, for a present's program to read: a[0]
				// the program's slot, a[1] the texture's (one OP_FX_TEXTURE made, not among those
				// read), a[2] how many parameters, a[3] the textures it reads, as OP_FX_PRESENT's;
				// a[4], a[5] the rectangle drawn, x0 | y0 << 16 and x1 | y1 << 16 (0, 0: all of
				// it); fd: the parameters, the same buffer as OP_FX_PRESENT's, taken now
	OP_LIFT = 9,		// the gain every present from now draws its pixels with, for a backlight turned down
				// by as much (echod's screen/lift.go): a[0] the gain in 1/65536, 0 or 65536 and
				// under being none; a[1] bit 0: draw the last canvas again with it now, which is
				// -EAGAIN when the last present was not the canvas alone.
};

// struct color is OP_COLOR's buffer: a 3x3 matrix applied to the panel's R, G, B (row-major, out =
// m * in, 16.16 fixed point), then one curve a channel, 256 points over the input's range, 0..65535.
struct color {
	int32_t m[9];
	uint16_t curve[3][256];
};

struct req {
	uint32_t op;
	uint32_t a[7];
};

struct rep {
	int32_t status;		// 0, or a negative errno
	uint32_t a[3];
	char text[112];
};

static const char *log_name = "techo5-gpu";
static int debug; // TECHO5_GPU_DEBUG set: time each development's steps on stderr

static double now_ms(void)
{
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e3 + t.tv_nsec / 1e6;
}
#define say(...) do { fprintf(stderr, "%s: ", log_name); fprintf(stderr, __VA_ARGS__); fputc('\n', stderr); } while (0)

// ---- shared buffers ----

// MAX_SIDE caps every picture's width and height (and a raw frame's bytes a line at twice it), so the
// sizes worked out from a request, done in 64 bits, stay far from overflowing anything.
#define MAX_SIDE 8192

struct shm {
	int fd;
	void *p;
	size_t size;
};

// shm_take puts the memfd *fd, when one was passed, in m's place, and takes it: *fd becomes -1, and
// it is closed here if it cannot be used. Either way m must then hold need bytes. Nothing of m changes
// unless the new buffer is mapped and big enough: a request refused leaves what was there.
static int shm_take(struct shm *m, int *fd, uint64_t need)
{
	if (*fd < 0)
		return m->p && m->size >= need ? 0 : -EINVAL;
	int f = *fd;
	*fd = -1;
	off_t size = lseek(f, 0, SEEK_END);
	if (size <= 0 || (uint64_t)size < need || (uint64_t)size > SIZE_MAX) {
		close(f);
		return -EINVAL;
	}
	void *p = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, f, 0);
	if (p == MAP_FAILED) {
		int err = -errno;
		close(f);
		return err;
	}
	if (m->p)
		munmap(m->p, m->size);
	if (m->fd >= 0)
		close(m->fd);
	m->fd = f, m->p = p, m->size = size;
	return 0;
}

// side_ok is whether a width or height is one a request may ask for.
static int side_ok(uint32_t v) { return v > 0 && v <= MAX_SIDE; }

// ---- GL ----

// gl_log is what the compiler last said against a shader, for a reply to carry.
static char gl_log[1024];

static GLuint shader(GLenum type, const char *src)
{
	GLuint s = glCreateShader(type);
	glShaderSource(s, 1, &src, NULL);
	glCompileShader(s);
	GLint ok;
	glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
	if (!ok) {
		glGetShaderInfoLog(s, sizeof(gl_log), NULL, gl_log);
		say("shader: %s", gl_log);
		glDeleteShader(s);
		return 0;
	}
	return s;
}

// program links vs and fs, fs with defines ("#define ..." lines, or "") ahead of it.
static GLuint program(const char *vs, const char *defines, const char *fs)
{
	char *src = malloc(strlen(defines) + strlen(fs) + 1);
	if (!src)
		return 0;
	strcpy(src, defines);
	strcat(src, fs);
	GLuint v = shader(GL_VERTEX_SHADER, vs), f = shader(GL_FRAGMENT_SHADER, src);
	free(src);
	if (!v || !f)
		return 0;
	GLuint p = glCreateProgram();
	glAttachShader(p, v);
	glAttachShader(p, f);
	glBindAttribLocation(p, 0, "pos");
	glLinkProgram(p);
	GLint ok;
	glGetProgramiv(p, GL_LINK_STATUS, &ok);
	if (!ok) {
		glGetProgramInfoLog(p, sizeof(gl_log), NULL, gl_log);
		say("link: %s", gl_log);
		glDeleteProgram(p);
		return 0;
	}
	return p;
}

// One full-target quad. The fragment shaders work out their pixel from uv, the target's coordinates
// from 0 to 1, bottom-up, interpolated at full precision. Not from gl_FragCoord: OpenGL ES 2.0 gives it
// only medium precision, a 16-bit float on these GPUs, which cannot hold x + 0.5 past 1024, so on the
// Show 8 every odd column beyond 1024 came out as its even neighbor.
//
// flip turns uv upside down, for a target whose first row in memory is the panel's top one (the
// framebuffer's panel): the shaders below go on taking uv as a window's, bottom-up.
static const char *vs_quad =
	"attribute vec2 pos;\n"
	"uniform float flip;\n"
	"varying highp vec2 uv;\n"
	"void main() {\n"
	"  uv = pos * 0.5 + 0.5;\n"
	"  if (flip > 0.5) uv.y = 1.0 - uv.y;\n"
	"  gl_Position = vec4(pos, 0.0, 1.0);\n"
	"}\n";

static void quad(void)
{
	static const GLfloat v[] = { -1, -1, 1, -1, -1, 1, 1, 1 };
	glVertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 0, v);
	glEnableVertexAttribArray(0);
	glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
}

// Rows. GL counts the window's rows from the bottom (gl_FragCoord.y), while the panel scans out from the
// top and textures hold their first uploaded row, the top one, at t = 0. So the panel row from the top
// is panelh - gl_FragCoord.y, and texture coordinates are taken in top-down picture coordinates. (The
// camera passes draw into a texture and read it back in the same row order, so they need no turn.)
//
// The lift: with the backlight turned down to 1/gain^2.2 of what was asked, every pixel is drawn gain
// times as bright, so the panel shows what it would have. Drawn to an 8-bit buffer, a plain gain would
// skip a code every 1/(gain-1) of them, a step that can show in a smooth dark gradient; an ordered dither
// (4x4 Bayer, from the panel pixel p) puts each pixel on one of its two neighboring codes instead, on
// average at the exact value. The code is taken back to its integer first: a sampler's 8-bit value is
// not exactly k/255 on every GPU (llvmpipe's is a little under), and the dither's margin is 1/32. A gain of 1 leaves the pixel exactly as it was. Nothing clips that the
// gain was chosen for (echod keeps the canvas's brightest value times the gain at 255 or under); what is
// over is held at 255.
//
// The two programs that draw the panel's pixels come in variants, picked for each frame by what it
// needs (panel_program): LIFT when the gain is over 1, TONE when a color matrix is applied here, and,
// for the canvas, OVER when it is the controls blended over a frame. A shader that tests for these as
// it runs pays for them on every pixel all the same, a GPU having no cheap branches: on the 1st gen
// Show 5's Mali-T720 the canvas took 12 ms that way and 3 ms with nothing to test.
#define LIFT_GLSL \
	"uniform float gain;\n" \
	"float bayer2(vec2 a) { return 2.0 * a.x + 3.0 * a.y - 4.0 * a.x * a.y; }\n" \
	"vec3 lift(vec3 c, vec2 p) {\n" \
	"  vec2 a = mod(floor(p), 4.0);\n" \
	"  float m = 4.0 * bayer2(mod(a, 2.0)) + bayer2(floor(a / 2.0));\n" \
	"  vec3 k = floor(c * 255.0 + 0.5);\n" \
	"  return min(floor(k * gain + (m + 0.5) / 16.0), 255.0) / 255.0;\n" \
	"}\n"

// The color matrix (OP_COLOR) where the display pipeline has no block for it: applied to what goes to
// the panel, after the lift. It is linear, so the canvas's controls blended over a frame can each be
// put through it before the blend.
#define TONE_GLSL \
	"uniform mat3 ctm;\n" \
	"vec3 tone(vec3 c) { return clamp(ctm * c, 0.0, 1.0); }\n"

// The canvas onto the panel: panel pixel (px, py) shows canvas (py, panelW-1-px) when rotated, as
// hardware/screen's rotate does. The canvas is premultiplied RGBA; the panel has no alpha.
static const char *fs_canvas =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D canvas;\n"
	"uniform vec2 csize;\n"		// canvas w, h
	"uniform float panelw;\n"
	"uniform float panelh;\n"
	"uniform float rotated;\n"
	"uniform vec4 clip;\n"		// OVER: canvas x0, y0, x1, y1 to draw, its alpha kept for the blend
	LIFT_GLSL
	TONE_GLSL
	"void main() {\n"
	"  vec2 p = vec2(uv.x * panelw, (1.0 - uv.y) * panelh);\n"
	"  vec2 c = rotated > 0.5 ? vec2(p.y, panelw - p.x) : p;\n"
	"#ifdef OVER\n"
	"  if (c.x < clip.x || c.y < clip.y || c.x >= clip.z || c.y >= clip.w) discard;\n"
	"#endif\n"
	"  vec4 t = texture2D(canvas, c / csize);\n"
	"  vec3 v = t.rgb;\n"
	"#ifdef LIFT\n"
	"#ifdef OVER\n"
	"  v *= gain;\n"
	"#else\n"
	"  v = lift(v, p);\n"
	"#endif\n"
	"#endif\n"
	"#ifdef TONE\n"
	"  v = tone(v);\n"
	"#endif\n"
	"#ifdef OVER\n"
	"  gl_FragColor = vec4(v, t.a);\n"
	"#else\n"
	"  gl_FragColor = vec4(v, 1.0);\n"
	"#endif\n"
	"}\n";

// A video frame, already in the panel's orientation, top row first like the canvas.
static const char *fs_frame =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D frame;\n"
	"uniform float panelw;\n"
	"uniform float panelh;\n"
	"uniform float bgra;\n"	// 1: the frame's bytes are B, G, R, A
	LIFT_GLSL
	TONE_GLSL
	"void main() {\n"
	"  vec2 p = vec2(uv.x * panelw, (1.0 - uv.y) * panelh);\n"
	"  vec3 c = texture2D(frame, vec2(uv.x, 1.0 - uv.y)).rgb;\n"
	"  vec3 v = bgra > 0.5 ? c.bgr : c;\n"
	"#ifdef LIFT\n"
	"  v = lift(v, p);\n"
	"#endif\n"
	"#ifdef TONE\n"
	"  v = tone(v);\n"
	"#endif\n"
	"  gl_FragColor = vec4(v, 1.0);\n"
	"}\n";

// The camera, in two passes. Unpack: the raw frame is a LUMINANCE texture of its bytes, bpl wide, four
// 10-bit samples in five bytes, least significant bits first, so sample k of a group is made of bytes k
// and k+1 of it: floor(lo / 4^k) + mod(hi, 4^(k+1)) * 256 / 4^k, with no branch on k. Each sample is
// unpacked once, into an RGBA texture of the frame's size (high eight bits in r, low two in g), rather
// than by every neighbor that reads it.
static const char *fs_unpack =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D raw;\n"
	"uniform vec2 size;\n"		// w, h in pixels
	"uniform float bpl;\n"
	"void main() {\n"
	"  float x = floor(uv.x * size.x), y = (floor(uv.y * size.y) + 0.5) / size.y;\n"
	"  float grp = floor(x / 4.0), k = x - grp * 4.0, b = grp * 5.0 + k;\n"
	"  float lo = floor(texture2D(raw, vec2((b + 0.5) / bpl, y)).r * 255.0 + 0.5);\n"
	"  float hi = floor(texture2D(raw, vec2((b + 1.5) / bpl, y)).r * 255.0 + 0.5);\n"
	"  float p = exp2(2.0 * k);\n"
	"  float v = floor(lo / p) + mod(hi, 4.0 * p) * (256.0 / p);\n"
	"  float h8 = floor(v / 4.0);\n"
	"  gl_FragColor = vec4(h8 / 255.0, (v - h8 * 4.0) / 255.0, 0.0, 1.0);\n"
	"}\n";

// Develop: bilinear demosaic of the unpacked samples (edges mirrored, as Full does), then the tone
// tables, a 2048x3 LUMINANCE texture indexed by twice a sample (they run on the 11-bit scale of a
// summed green pair). Nine reads a pixel, and the colors picked by the pixel's place in its Bayer
// cell with arithmetic, not branches: neighboring pixels sit at all four places, and a GPU running
// them together would otherwise pay for every branch on each.
static const char *fs_develop =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D samples;\n"
	"uniform sampler2D tables;\n"
	"uniform vec2 size;\n"
	"uniform float bluefirst;\n"
	"float at(float x, float y) {\n"
	"  x = x < 0.0 ? 1.0 : (x >= size.x ? size.x - 2.0 : x);\n"
	"  y = y < 0.0 ? 1.0 : (y >= size.y ? size.y - 2.0 : y);\n"
	"  vec2 t = texture2D(samples, (vec2(x, y) + 0.5) / size).rg;\n"
	"  return floor(t.r * 255.0 + 0.5) * 4.0 + floor(t.g * 255.0 + 0.5);\n"
	"}\n"
	"float lut(float row, float v) { return texture2D(tables, vec2((2.0 * v + 0.5) / 2048.0, (row + 0.5) / 3.0)).r; }\n"
	"void main() {\n"
	"  float x = floor(uv.x * size.x), y = floor(uv.y * size.y);\n"
	"  float c = at(x, y);\n"
	"  float l = at(x - 1.0, y), r = at(x + 1.0, y), u = at(x, y - 1.0), d = at(x, y + 1.0);\n"
	"  float ul = at(x - 1.0, y - 1.0), ur = at(x + 1.0, y - 1.0), dl = at(x - 1.0, y + 1.0), dr = at(x + 1.0, y + 1.0);\n"
	"  float hor = floor((l + r) / 2.0), ver = floor((u + d) / 2.0);\n"
	"  float cross = floor((l + r + u + d) / 4.0), diag = floor((ul + ur + dl + dr) / 4.0);\n"
	"  float ox = mod(x, 2.0), oy = mod(y, 2.0);\n"
	"  float s0 = (1.0 - ox) * (1.0 - oy), s1 = ox * (1.0 - oy), s2 = (1.0 - ox) * oy, s3 = ox * oy;\n"
	// red site: (c, cross, diag); green on a red row: (hor, c, ver); green on a blue row: (ver, c,
	// hor); blue site: (diag, cross, c), before any swap of red and blue.
	"  vec3 rgb = s0 * vec3(c, cross, diag) + s1 * vec3(hor, c, ver) + s2 * vec3(ver, c, hor) + s3 * vec3(diag, cross, c);\n"
	"  if (bluefirst > 0.5) rgb = rgb.bgr;\n"
	"  gl_FragColor = vec4(lut(0.0, rgb.r), lut(1.0, rgb.g), lut(2.0, rgb.b), 1.0);\n"
	"}\n";

// RGBA to YUYV: one output texel per two pixels, (Y0, Cb, Y1, Cr), BT.601 full range as JFIF has it,
// the pair's chroma their average; the bytes read back are the encoder's YUYV as they are.
static const char *fs_yuyv =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D rgb;\n"
	"uniform vec2 size;\n"		// the RGBA picture's w, h
	"void main() {\n"
	"  float x = floor(uv.x * size.x * 0.5) * 2.0, y = floor(uv.y * size.y) + 0.5;\n"
	"  vec3 a = texture2D(rgb, vec2((x + 0.5) / size.x, y / size.y)).rgb;\n"
	"  vec3 b = texture2D(rgb, vec2((x + 1.5) / size.x, y / size.y)).rgb;\n"
	"  float y0 = dot(a, vec3(0.299, 0.587, 0.114)), y1 = dot(b, vec3(0.299, 0.587, 0.114));\n"
	"  vec3 m = (a + b) * 0.5;\n"
	"  float cb = dot(m, vec3(-0.168736, -0.331264, 0.5)) + 128.0 / 255.0;\n"
	"  float cr = dot(m, vec3(0.5, -0.418688, -0.081312)) + 128.0 / 255.0;\n"
	"  gl_FragColor = vec4(y0, cb, y1, cr);\n"
	"}\n";

// YUV 4:2:0, one plane a pass: each output texel is four bytes of the plane, so a plane of pw x ph
// bytes is drawn pw/4 x ph and read back as it is. plane 0 is Y; 1 and 2 are Cb and Cr, each the
// average of a 2x2 block of pixels. BT.601 full range, as JFIF has it.
static const char *fs_planar =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D rgb;\n"
	"uniform vec2 size;\n"		// the RGBA picture's w, h
	"uniform float plane;\n"
	"vec3 px(float x, float y) { return texture2D(rgb, (vec2(x, y) + 0.5) / size).rgb; }\n"
	"float value(vec3 c) {\n"
	"  if (plane < 0.5) return dot(c, vec3(0.299, 0.587, 0.114));\n"
	"  if (plane < 1.5) return dot(c, vec3(-0.168736, -0.331264, 0.5)) + 128.0 / 255.0;\n"
	"  return dot(c, vec3(0.5, -0.418688, -0.081312)) + 128.0 / 255.0;\n"
	"}\n"
	"void main() {\n"
	"  vec4 o;\n"
	"  if (plane < 0.5) {\n"
	"    float x = floor(uv.x * size.x / 4.0) * 4.0, y = floor(uv.y * size.y);\n"
	"    o = vec4(value(px(x, y)), value(px(x + 1.0, y)), value(px(x + 2.0, y)), value(px(x + 3.0, y)));\n"
	"  } else {\n"
	"    float x = floor(uv.x * size.x / 8.0) * 8.0, y = floor(uv.y * size.y / 2.0) * 2.0;\n"
	"    vec4 v;\n"
	"    for (int i = 0; i < 4; i++) {\n"
	"      float cx = x + float(i) * 2.0;\n"
	"      v[i] = value((px(cx, y) + px(cx + 1.0, y) + px(cx, y + 1.0) + px(cx + 1.0, y + 1.0)) * 0.25);\n"
	"    }\n"
	"    o = v;\n"
	"  }\n"
	"  gl_FragColor = o;\n"
	"}\n";

// What an effect may have: programs, textures (its samplers t0 to t7) and parameters (vec4s, p[]).
#define FX_PROGRAMS 8
#define FX_TEXTURES 8
#define FX_PARAMS 32

// Extension entry points, looked up at start: the libraries do not export them to link against.
static PFNEGLCREATEIMAGEKHRPROC create_image;
static PFNEGLDESTROYIMAGEKHRPROC destroy_image;
static PFNGLEGLIMAGETARGETTEXTURE2DOESPROC image_texture;

static struct {
	EGLDisplay dpy;
	EGLContext ctx;
	EGLConfig cfg;
	struct gbm_device *gbm;
	int drm;
	char gpu[64];

	// screen
	int open;
	uint32_t pw, ph;	// the panel's size
	drmModeModeInfo mode;
	uint32_t conn, crtc;
	struct gbm_surface *surf;
	EGLSurface esurf;
	struct gbm_bo *shown;
	uint32_t shown_fb;
	int crtc_set;
	struct shm canvas;
	GLuint canvas_tex;
	uint32_t cw, ch, rotated;
	GLuint p_panel[2][8]; // the canvas's and the frame's programs, by variant (panel_program)
	struct shm frame;
	GLuint frame_tex;
	uint32_t fw, fh;
	// the CRTC's color properties (0 when it has none), and whether either was ever set
	uint32_t ctm_prop, lut_prop, lut_size;
	int ctm_set, lut_set;
	struct shm color;
	// the lift's gain (OP_LIFT), and whether the last present was the canvas alone, which OP_LIFT
	// can draw again from its texture
	float gain;
	int canvas_shown;

	// the framebuffer's panel, where there is no DRM: the device (-1 on DRM), its pages mapped, a
	// line's bytes, how many pages and which is on show, and whether its bytes are B, G, R, A
	int fb;
	struct fb_var_screeninfo fb_var;
	uint8_t *fb_mem;
	size_t fb_size;
	uint32_t fb_line, fb_pages, fb_page;
	int fb_bgra, fb_test;
	// the picture the panel's pixels are drawn into, and the texture and framebuffer object it is
	// to GL; page_img when the GPU draws into its memory where it is, else it is read back
	struct shm page;
	EGLImageKHR page_img;
	GLuint page_tex, page_fbo;
	// the frame as an image the GPU reads where it is (nothing is uploaded), or none
	EGLImageKHR frame_img;
	// the color matrix (OP_COLOR), column by column as GL takes it, where the shaders apply it
	float ctm[9];
	int ctm_on;

	// effects: each program's source and its variants (by V_LIFT and V_TONE), the textures and their
	// pixels, the parameters' buffer, and the frame in hand: its program, its parameters as they were
	// when it was asked for, and the textures it reads
	char *fx_src[FX_PROGRAMS];
	int fx_pass[FX_PROGRAMS]; // a pass's program: its one variant is fx_prog[][0]
	GLuint fx_prog[FX_PROGRAMS][4];
	GLuint fx_fbo;
	struct shm fx_mem[FX_TEXTURES];
	GLuint fx_tex[FX_TEXTURES];
	uint32_t fx_w[FX_TEXTURES], fx_h[FX_TEXTURES];
	struct shm fx_params;
	float fx_p[FX_PARAMS * 4];
	uint32_t fx_slot, fx_n, fx_reads;

	// camera
	GLuint p_unpack, p_develop, p_yuyv, p_planar;
	struct shm raw, tables, out;
	GLuint raw_tex, tab_tex, smp_tex, rgb_tex, yuyv_tex, smp_fbo, rgb_fbo, yuyv_fbo;
	GLuint plane_tex[3], plane_fbo[3];
	uint32_t plw, plh; // the size the plane targets were made for
	// the dmabuf YUYV target, for its size
	struct gbm_bo *dma_bo;
	EGLImageKHR dma_img;
	GLuint dma_tex, dma_fbo;
	uint32_t dma_w, dma_h;
	uint32_t dw, dh, dbpl;
} g = { .drm = -1, .canvas.fd = -1, .frame.fd = -1, .raw.fd = -1, .tables.fd = -1, .out.fd = -1,
	.color.fd = -1, .gain = 1, .fb = -1, .page.fd = -1, .fx_params.fd = -1,
	.fx_mem = { { .fd = -1 }, { .fd = -1 }, { .fd = -1 }, { .fd = -1 }, { .fd = -1 }, { .fd = -1 }, { .fd = -1 }, { .fd = -1 } } };

static GLuint texture(void)
{
	GLuint t;
	glGenTextures(1, &t);
	glBindTexture(GL_TEXTURE_2D, t);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
	return t;
}

// The panel's DRM device: the card with a connected connector. GBM on it is Mesa's kmsro, which
// renders on panfrost and allocates buffers the display can scan out.
static int open_card(void)
{
	char path[32];
	for (int i = 0; i < 8; i++) {
		snprintf(path, sizeof(path), "/dev/dri/card%d", i);
		int fd = open(path, O_RDWR | O_CLOEXEC);
		if (fd < 0)
			continue;
		drmModeRes *res = drmModeGetResources(fd);
		int found = 0;
		for (int c = 0; res && c < res->count_connectors && !found; c++) {
			drmModeConnector *conn = drmModeGetConnector(fd, res->connectors[c]);
			if (conn && conn->connection == DRM_MODE_CONNECTED && conn->count_modes > 0)
				found = 1;
			drmModeFreeConnector(conn);
		}
		drmModeFreeResources(res);
		if (found)
			return fd;
		close(fd);
	}
	return -ENODEV;
}

// panel_program is the program that draws the canvas (frame 0) or a video frame (1) onto the panel
// for what the frame needs now: the lift, the color matrix, and over, the canvas as controls over a
// frame. Each variant is compiled the first time it is asked for; 0 when it cannot be.
enum { V_LIFT = 1, V_TONE = 2, V_OVER = 4 };

static GLuint panel_program(int frame, int over)
{
	int v = (g.gain > 1 ? V_LIFT : 0) | (g.ctm_on ? V_TONE : 0) | (over && !frame ? V_OVER : 0);
	if (!g.p_panel[frame][v]) {
		char defines[64];
		snprintf(defines, sizeof(defines), "%s%s%s", v & V_LIFT ? "#define LIFT\n" : "",
			 v & V_TONE ? "#define TONE\n" : "", v & V_OVER ? "#define OVER\n" : "");
		g.p_panel[frame][v] = program(vs_quad, defines, frame ? fs_frame : fs_canvas);
	}
	return g.p_panel[frame][v];
}

static int fb_start(void);

static int gl_start(void)
{
	PFNEGLGETPLATFORMDISPLAYEXTPROC get = (void *)eglGetProcAddress("eglGetPlatformDisplayEXT");
	// A test's framebuffer is the panel wherever this runs: on a desk too, whose own screen is not
	// to be taken.
	g.drm = getenv("TECHO5_GPU_FB_TEST") ? -ENODEV : open_card();
	if (g.drm < 0) {
		// No DRM: the framebuffer's panel, and GL with no window system under it.
		int err = fb_start();
		if (err) {
			say("no DRM card with a panel, and no framebuffer to draw one on");
			return err;
		}
		g.dpy = get ? get(EGL_PLATFORM_SURFACELESS_MESA, EGL_DEFAULT_DISPLAY, NULL) : EGL_NO_DISPLAY;
	} else {
		g.gbm = gbm_create_device(g.drm);
		if (!g.gbm)
			return -ENODEV;
		g.dpy = get ? get(EGL_PLATFORM_GBM_KHR, g.gbm, NULL) : eglGetDisplay((EGLNativeDisplayType)g.gbm);
	}
	if (g.dpy == EGL_NO_DISPLAY || !eglInitialize(g.dpy, NULL, NULL))
		return -ENODEV;
	eglBindAPI(EGL_OPENGL_ES_API);
	EGLint attrs[] = { EGL_SURFACE_TYPE, g.fb >= 0 ? EGL_PBUFFER_BIT : EGL_WINDOW_BIT, EGL_RED_SIZE, 8, EGL_GREEN_SIZE, 8,
		EGL_BLUE_SIZE, 8, EGL_ALPHA_SIZE, 0, EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT, EGL_NONE };
	EGLConfig cfgs[64];
	EGLint n = 0;
	eglChooseConfig(g.dpy, attrs, cfgs, 64, &n);
	// The config whose native visual is the scanout format; with no window system, where everything
	// is drawn into textures, any will do.
	for (int i = 0; i < n; i++) {
		EGLint id;
		eglGetConfigAttrib(g.dpy, cfgs[i], EGL_NATIVE_VISUAL_ID, &id);
		if (id == GBM_FORMAT_XRGB8888 || g.fb >= 0) {
			g.cfg = cfgs[i];
			break;
		}
	}
	if (!g.cfg)
		return -ENODEV;
	EGLint cattrs[] = { EGL_CONTEXT_CLIENT_VERSION, 2, EGL_NONE };
	g.ctx = eglCreateContext(g.dpy, g.cfg, EGL_NO_CONTEXT, cattrs);
	if (g.ctx == EGL_NO_CONTEXT || !eglMakeCurrent(g.dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, g.ctx))
		return -ENODEV;
	create_image = (void *)eglGetProcAddress("eglCreateImageKHR");
	destroy_image = (void *)eglGetProcAddress("eglDestroyImageKHR");
	image_texture = (void *)eglGetProcAddress("glEGLImageTargetTexture2DOES");
	glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
	glPixelStorei(GL_PACK_ALIGNMENT, 1);
	g.p_unpack = program(vs_quad, "", fs_unpack);
	g.p_develop = program(vs_quad, "", fs_develop);
	g.p_yuyv = program(vs_quad, "", fs_yuyv);
	g.p_planar = program(vs_quad, "", fs_planar);
	if (!panel_program(0, 0) || !panel_program(1, 0) || !g.p_unpack || !g.p_develop || !g.p_yuyv || !g.p_planar)
		return -EINVAL;
	snprintf(g.gpu, sizeof(g.gpu), "%s", (const char *)glGetString(GL_RENDERER));
	return 0;
}

// ---- the screen ----

static void flip_done(int fd, unsigned int seq, unsigned int s, unsigned int us, void *data)
{
	*(int *)data = 0;
}

static uint32_t bo_fb(struct gbm_bo *bo)
{
	uint32_t *fb = gbm_bo_get_user_data(bo);
	if (fb)
		return *fb;
	fb = malloc(sizeof(*fb));
	if (!fb)
		return 0;
	uint32_t handles[4] = { gbm_bo_get_handle(bo).u32 }, pitches[4] = { gbm_bo_get_stride(bo) }, offsets[4] = { 0 };
	if (drmModeAddFB2(g.drm, gbm_bo_get_width(bo), gbm_bo_get_height(bo), GBM_FORMAT_XRGB8888,
			  handles, pitches, offsets, fb, 0)) {
		free(fb);
		return 0;
	}
	gbm_bo_set_user_data(bo, fb, NULL);
	return *fb;
}

static void color_props(void);
static int set_color(uint32_t flags, const struct color *c);

// kms_open finds the panel's connector, CRTC and mode, and makes the window the panel is drawn in.
static int kms_open(void)
{
	drmModeRes *res = drmModeGetResources(g.drm);
	if (!res)
		return -ENODEV;
	drmModeConnector *conn = NULL;
	for (int c = 0; c < res->count_connectors; c++) {
		conn = drmModeGetConnector(g.drm, res->connectors[c]);
		if (conn && conn->connection == DRM_MODE_CONNECTED && conn->count_modes)
			break;
		drmModeFreeConnector(conn);
		conn = NULL;
	}
	if (!conn) {
		drmModeFreeResources(res);
		return -ENODEV;
	}
	drmModeEncoder *enc = conn->encoder_id ? drmModeGetEncoder(g.drm, conn->encoder_id) : NULL;
	uint32_t crtc = enc && enc->crtc_id ? enc->crtc_id : res->count_crtcs > 0 ? res->crtcs[0] : 0;
	drmModeFreeEncoder(enc);
	if (!crtc) {
		drmModeFreeConnector(conn);
		drmModeFreeResources(res);
		return -ENODEV;
	}
	g.mode = conn->modes[0];
	for (int m = 0; m < conn->count_modes; m++)
		if (conn->modes[m].type & DRM_MODE_TYPE_PREFERRED)
			g.mode = conn->modes[m];
	g.conn = conn->connector_id;
	g.crtc = crtc;
	drmModeFreeConnector(conn);
	drmModeFreeResources(res);
	g.surf = gbm_surface_create(g.gbm, g.mode.hdisplay, g.mode.vdisplay, GBM_FORMAT_XRGB8888,
				    GBM_BO_USE_SCANOUT | GBM_BO_USE_RENDERING);
	if (!g.surf)
		return -ENOMEM;
	g.esurf = eglCreateWindowSurface(g.dpy, g.cfg, (EGLNativeWindowType)g.surf, NULL);
	if (g.esurf == EGL_NO_SURFACE) {
		gbm_surface_destroy(g.surf);
		g.surf = NULL;
		return -ENOMEM;
	}
	g.pw = g.mode.hdisplay, g.ph = g.mode.vdisplay;
	return 0;
}

static int fb_open(void);
static int panel(void);
static EGLImageKHR image_of(int fd, uint32_t w, uint32_t h, int bgra);

// The canvas is drawn from a copy taken at each present, its texture's own storage, never from echod's
// memory where it is, on the framebuffer's panel either. echod goes on drawing the next frame while
// this one is on its way to the panel (an early present), and the lift draws the last one again from
// the copy. And the kernel driver there (kbase) ends a job by dropping whatever the processor has
// written, and not yet let out of its caches, to memory it lent the GPU for the job: strokes echod
// made meanwhile would be lost from the canvas itself. A copy into a buffer of this process's own for
// the GPU to read in place was tried and is no quicker than the upload (2.3 ms against 2.1 for the
// Show 5's canvas).
static void canvas_take(void)
{
	glBindTexture(GL_TEXTURE_2D, g.canvas_tex);
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, g.cw, g.ch, GL_RGBA, GL_UNSIGNED_BYTE, g.canvas.p);
}

static int display_open(struct req *r, int *fd, struct rep *out)
{
	uint32_t w = r->a[0], h = r->a[1];
	if (!side_ok(w) || !side_ok(h))
		return -EINVAL;
	int err = shm_take(&g.canvas, fd, (uint64_t)w * h * 4);
	if (err)
		return err;
	if (!g.open) {
		if ((err = g.fb >= 0 ? fb_open() : kms_open()))
			return err;
		g.canvas_tex = texture();
		g.open = 1;
		if (g.fb < 0) {
			color_props();
			if (g.ctm_set || g.lut_set)
				set_color(0, NULL);
		}
	}
	g.cw = w, g.ch = h, g.rotated = r->a[2] & 1;
	if ((err = panel()))
		return err;
	if (g.fb >= 0 && !g.fb_test) {
		// echod may have shown pages of its own since this last did.
		struct fb_var_screeninfo v;
		if (!ioctl(g.fb, FBIOGET_VSCREENINFO, &v))
			g.fb_page = v.yoffset / g.ph < g.fb_pages ? v.yoffset / g.ph : 0;
	}
	glBindTexture(GL_TEXTURE_2D, g.canvas_tex);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, g.cw, g.ch, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
	g.canvas_shown = 0;
	out->a[0] = g.pw, out->a[1] = g.ph, out->a[2] = g.fb >= 0 ? 60 : g.mode.vrefresh;
	return 0;
}

// vblank_running is whether the CRTC's vblank counter moves: three frames' time and more at 60 Hz.
// A driver without the sequence ioctl is taken at its word.
static int vblank_running(void)
{
	uint64_t a, b, ns;
	if (drmCrtcGetSequence(g.drm, g.crtc, &a, &ns))
		return 1;
	usleep(60000);
	if (drmCrtcGetSequence(g.drm, g.crtc, &b, &ns))
		return 1;
	return b != a;
}

// show swaps the drawn buffer onto the panel: the first time with a mode set, after that with a page
// flip, waiting for it so the caller is paced by the panel and the buffer it frees is truly free.
static int fb_show(void);

static int show(void)
{
	if (g.fb >= 0)
		return fb_show();
	if (!eglSwapBuffers(g.dpy, g.esurf))
		return -EIO;
	struct gbm_bo *bo = gbm_surface_lock_front_buffer(g.surf);
	if (!bo)
		return -EIO;
	uint32_t fb = bo_fb(bo);
	if (!fb) {
		gbm_surface_release_buffer(g.surf, bo);
		return -EIO;
	}
	int err;
	if (!g.crtc_set) {
		err = drmModeSetCrtc(g.drm, g.crtc, fb, 0, 0, &g.conn, 1, &g.mode) ? -errno : 0;
		// Said, for the log: a pipeline that never starts (the Show 5's DSI could stall at the first
		// enable after the bootloader's handover, before patches/0016) shows here first. Turning the
		// CRTC off and on does not bring it back, so nothing more is tried.
		if (!err && !vblank_running())
			say("no vblank after the first modeset: the display pipeline has not started");
		g.crtc_set = !err;
	} else {
		static int pending; // static: a flip's event can come after this frame has given up on it
		drmEventContext ev = { .version = 2, .page_flip_handler = flip_done };
		pending = 1;
		err = drmModePageFlip(g.drm, g.crtc, fb, DRM_MODE_PAGE_FLIP_EVENT, &pending) ? -errno : 0;
		while (!err && pending) {
			struct pollfd p = { .fd = g.drm, .events = POLLIN };
			if (poll(&p, 1, 200) <= 0)
				break;
			drmHandleEvent(g.drm, &ev);
		}
	}
	if (g.shown)
		gbm_surface_release_buffer(g.surf, g.shown);
	g.shown = bo;
	return err;
}

// panel makes the panel's pixels what is drawn on next: the window on DRM, else the page's picture.
static int panel(void)
{
	if (g.fb >= 0) {
		if (!eglMakeCurrent(g.dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, g.ctx))
			return -EIO;
		glBindFramebuffer(GL_FRAMEBUFFER, g.page_fbo);
	} else {
		if (!eglMakeCurrent(g.dpy, g.esurf, g.esurf, g.ctx))
			return -EIO;
		glBindFramebuffer(GL_FRAMEBUFFER, 0);
	}
	glViewport(0, 0, g.pw, g.ph);
	glDisable(GL_BLEND);
	return 0;
}

// panel_uniforms gives prog, one of the two that draw the panel's pixels, what both take.
// A variant has only the uniforms it uses; the others' locations are -1, which GL ignores.
static void panel_uniforms(GLuint prog)
{
	glUniform1f(glGetUniformLocation(prog, "panelw"), g.pw);
	glUniform1f(glGetUniformLocation(prog, "panelh"), g.ph);
	glUniform1f(glGetUniformLocation(prog, "gain"), g.gain);
	glUniform1f(glGetUniformLocation(prog, "flip"), g.fb >= 0);
	glUniformMatrix3fv(glGetUniformLocation(prog, "ctm"), 1, GL_FALSE, g.ctm);
}

// draw_canvas draws the canvas onto the panel: all of it, or, with over, its rectangle x0, y0 to x1, y1
// with its alpha, for the blend over a frame. False if the program could not be made.
static int draw_canvas(int over, float x0, float y0, float x1, float y1)
{
	GLuint prog = panel_program(0, over);
	if (!prog)
		return 0;
	glUseProgram(prog);
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_2D, g.canvas_tex);
	glUniform1i(glGetUniformLocation(prog, "canvas"), 0);
	glUniform2f(glGetUniformLocation(prog, "csize"), g.cw, g.ch);
	glUniform1f(glGetUniformLocation(prog, "rotated"), g.rotated);
	glUniform4f(glGetUniformLocation(prog, "clip"), x0, y0, x1, y1);
	panel_uniforms(prog);
	quad();
	return 1;
}

// brightest is the highest red, green or blue value of n RGBA pixels: what the lift may raise to 255.
// 0.6 ms for the Show 8's 1280x800 canvas and about 0.9 ms for the Show 5's 960x480, read at about
// the memory's speed; only while the lift is on.
static unsigned brightest(const uint8_t *p, size_t n)
{
	size_t i = 0, bytes = n * 4;
	unsigned top = 0;
#if defined(__aarch64__) || defined(__ARM_NEON)
	static const uint8_t rgb[16] = { 255, 255, 255, 0, 255, 255, 255, 0, 255, 255, 255, 0, 255, 255, 255, 0 };
	uint8x16_t m0 = vdupq_n_u8(0), m1 = m0, m2 = m0, m3 = m0;
	for (; i + 64 <= bytes; i += 64) {
		m0 = vmaxq_u8(m0, vld1q_u8(p + i));
		m1 = vmaxq_u8(m1, vld1q_u8(p + i + 16));
		m2 = vmaxq_u8(m2, vld1q_u8(p + i + 32));
		m3 = vmaxq_u8(m3, vld1q_u8(p + i + 48));
	}
	m0 = vandq_u8(vmaxq_u8(vmaxq_u8(m0, m1), vmaxq_u8(m2, m3)), vld1q_u8(rgb));
#if defined(__aarch64__)
	top = vmaxvq_u8(m0);
#else
	uint8_t each[16]; // 32-bit NEON has no maximum across a vector
	vst1q_u8(each, m0);
	for (int k = 0; k < 16; k++)
		if (each[k] > top)
			top = each[k];
#endif
#endif
	for (; i < bytes; i++)
		if (i % 4 != 3 && p[i] > top)
			top = p[i];
	return top;
}

// later is what a request already answered still has to do (an early present's drawing), run before
// the next request is read.
static int (*later)(void);

// redraw draws the canvas last presented again, from its texture, with the gain as it is now.
static int redraw(void)
{
	if (!g.open || !g.canvas_shown)
		return -EAGAIN;
	if (panel())
		return -EIO;
	if (!draw_canvas(0, 0, 0, g.cw, g.ch))
		return -EINVAL;
	return show();
}

static int lift(struct req *r)
{
	uint32_t q = r->a[0];
	if (q > 4 * 65536)
		return -EINVAL;
	g.gain = q > 65536 ? q / 65536.0f : 1.0f;
	return (r->a[1] & 1) ? redraw() : 0;
}

static int present(struct req *r, struct rep *out)
{
	if (!g.open)
		return -ENODEV;
	if (panel())
		return -EIO;
	if (r->a[0] & 1)
		out->a[0] = brightest(g.canvas.p, (size_t)g.cw * g.ch) + 1;
	g.canvas_shown = 1;
	canvas_take();
	if (g.fb >= 0 && (r->a[0] & 3) == 2) {
		later = redraw;
		return 0;
	}
	if (!draw_canvas(0, 0, 0, g.cw, g.ch))
		return -EINVAL;
	return show();
}

static int present_frame(struct req *r, int *fd)
{
	if (!g.open)
		return -ENODEV;
	uint32_t w = r->a[0], h = r->a[1];
	if (!side_ok(w) || !side_ok(h))
		return -EINVAL;
	int had = g.frame.fd;
	int err = shm_take(&g.frame, fd, (uint64_t)w * h * 4);
	if (err)
		return err;
	if (panel())
		return -EIO;
	// The frame where it is, as the canvas is, when its buffer or size is new; else uploaded.
	if (g.frame.fd != had || w != g.fw || h != g.fh) {
		if (g.frame_tex)
			glDeleteTextures(1, &g.frame_tex);
		g.frame_tex = texture();
		if (g.frame_img != EGL_NO_IMAGE_KHR)
			destroy_image(g.dpy, g.frame_img);
		g.frame_img = g.fb >= 0 ? image_of(g.frame.fd, w, h, 0) : EGL_NO_IMAGE_KHR;
		if (g.frame_img != EGL_NO_IMAGE_KHR) {
			image_texture(GL_TEXTURE_2D, g.frame_img);
			if (glGetError() != GL_NO_ERROR) {
				destroy_image(g.dpy, g.frame_img);
				g.frame_img = EGL_NO_IMAGE_KHR;
			}
		}
		if (g.frame_img == EGL_NO_IMAGE_KHR)
			glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
		g.fw = w, g.fh = h;
	}
	glBindTexture(GL_TEXTURE_2D, g.frame_tex);
	if (g.frame_img == EGL_NO_IMAGE_KHR)
		glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, w, h, GL_RGBA, GL_UNSIGNED_BYTE, g.frame.p);
	GLuint prog = panel_program(1, 0);
	if (!prog)
		return -EINVAL;
	glUseProgram(prog);
	glUniform1i(glGetUniformLocation(prog, "frame"), 0);
	glUniform1f(glGetUniformLocation(prog, "bgra"), r->a[6] & 1);
	panel_uniforms(prog);
	g.canvas_shown = 0;
	quad();
	if (r->a[4] > r->a[2] && r->a[5] > r->a[3]) {
		// The controls: the canvas's rectangle, premultiplied, over the frame.
		canvas_take();
		glEnable(GL_BLEND);
		glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA);
		int drawn = draw_canvas(1, r->a[2], r->a[3], r->a[4], r->a[5]);
		glDisable(GL_BLEND);
		if (!drawn)
			return -EINVAL;
	}
	return show();
}


static int blank(int off)
{
	if (!g.open)
		return -ENODEV;
	if (g.fb >= 0)
		return -ENOSYS; // the framebuffer's panel is echod's to switch, as it was
	drmModeConnector *conn = drmModeGetConnector(g.drm, g.conn);
	if (!conn)
		return -ENODEV;
	int err = -ENOENT;
	for (int i = 0; i < conn->count_props; i++) {
		drmModePropertyRes *p = drmModeGetProperty(g.drm, conn->props[i]);
		if (p && !strcmp(p->name, "DPMS"))
			err = drmModeConnectorSetProperty(g.drm, g.conn, p->prop_id,
							  off ? DRM_MODE_DPMS_OFF : DRM_MODE_DPMS_ON) ? -errno : 0;
		drmModeFreeProperty(p);
	}
	drmModeFreeConnector(conn);
	return err;
}

// ---- effects ----
//
// A frame echod has more to than its canvas: a light moving over the page, a card of frosted glass, a
// page pushed aside. Drawn on the cores those are most of what a moving screen costs (the Rainbow turn
// screen takes three of the Show 5's four cores); here they are a fragment shader of echod's own, run
// for every pixel of the panel over the canvas, which then holds the page alone and is copied only
// when the page changes. This knows nothing of any one effect: echod gives the shader's source, the
// pictures it reads and its numbers, and the look stays where the screen's code is.
//
// A program's source defines
//
//	vec3 fx(vec2 c)
//
// the color of the canvas's pixel at c (in canvas pixels, x across and y down, a pixel's middle at .5).
// It has page(c), the canvas there, read smoothly between pixels; csize, the canvas's size; p[], its
// parameters; and t0 to t7, its textures. Turning the canvas onto the panel, the lift and the color
// matrix are done round it here, as for a plain canvas.
//
// What need not be worked out at every pixel of the panel can be drawn first into a texture at a size
// of its own, by a pass: a program whose source defines
//
//	vec4 pass(vec2 c)
//
// the texture's pixel at c (in its pixels, x across and y down from its first row, a pixel's middle
// at .5), with the same page(c), csize (the canvas's), p[] and textures. A frame's passes are drawn as
// they are asked for, before its present.
static const char *fs_fx_head =
	"precision highp float;\n"
	"varying highp vec2 uv;\n"
	"uniform sampler2D canvas, t0, t1, t2, t3, t4, t5, t6, t7;\n"
	"uniform vec2 csize;\n"
	"uniform float panelw, panelh, rotated;\n"
	"uniform vec4 p[32];\n"
	LIFT_GLSL
	TONE_GLSL
	"vec3 page(vec2 c) { return texture2D(canvas, c / csize).rgb; }\n"
	"#line 1\n";

static const char *fs_pass_tail =
	"\nvoid main() {\n"
	"  gl_FragColor = pass(gl_FragCoord.xy);\n"
	"}\n";

static const char *fs_fx_tail =
	"\nvoid main() {\n"
	"  vec2 pp = vec2(uv.x * panelw, (1.0 - uv.y) * panelh);\n"
	"  vec2 c = rotated > 0.5 ? vec2(pp.y, panelw - pp.x) : pp;\n"
	"  vec3 v = fx(c);\n"
	"#ifdef LIFT\n"
	"  v = lift(v, pp);\n"
	"#endif\n"
	"#ifdef TONE\n"
	"  v = tone(v);\n"
	"#endif\n"
	"  gl_FragColor = vec4(v, 1.0);\n"
	"}\n";

// fx_program is slot's program for the lift and the color matrix as they are now, compiled the first
// time each is needed; 0 when it cannot be.
static GLuint fx_program(uint32_t slot)
{
	int v = g.fx_pass[slot] ? 0 : (g.gain > 1 ? V_LIFT : 0) | (g.ctm_on ? V_TONE : 0);
	if (!g.fx_src[slot])
		return 0;
	if (!g.fx_prog[slot][v]) {
		const char *tail = g.fx_pass[slot] ? fs_pass_tail : fs_fx_tail;
		size_t n = strlen(fs_fx_head) + strlen(g.fx_src[slot]) + strlen(tail) + 1;
		char *src = malloc(n);
		if (!src)
			return 0;
		snprintf(src, n, "%s%s%s", fs_fx_head, g.fx_src[slot], tail);
		g.fx_prog[slot][v] = program(vs_quad, v & V_LIFT ? (v & V_TONE ? "#define LIFT\n#define TONE\n" : "#define LIFT\n")
								 : (v & V_TONE ? "#define TONE\n" : ""), src);
		free(src);
	}
	return g.fx_prog[slot][v];
}

// fx_current makes GL's context current for work that draws nothing on the panel.
static int fx_current(void)
{
	if (g.open)
		return panel();
	return eglMakeCurrent(g.dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, g.ctx) ? 0 : -EIO;
}

static int fx_set_program(struct req *r, int *fd, struct rep *out)
{
	uint32_t slot = r->a[0];
	if (slot >= FX_PROGRAMS || *fd < 0)
		return -EINVAL;
	off_t size = lseek(*fd, 0, SEEK_END);
	if (size <= 0 || size > 1 << 20)
		return -EINVAL;
	char *src = malloc(size + 1);
	if (!src)
		return -ENOMEM;
	ssize_t n = pread(*fd, src, size, 0);
	if (n != size) {
		free(src);
		return -EIO;
	}
	src[size] = 0;
	if (fx_current()) {
		free(src);
		return -EIO;
	}
	for (int v = 0; v < 4; v++)
		if (g.fx_prog[slot][v]) {
			glDeleteProgram(g.fx_prog[slot][v]);
			g.fx_prog[slot][v] = 0;
		}
	free(g.fx_src[slot]);
	g.fx_src[slot] = src;
	g.fx_pass[slot] = r->a[1] & 1;
	// Compiled now, so that a shader this GPU's compiler will not take is refused here, with what
	// the compiler said, and not at the first frame it is wanted for.
	gl_log[0] = 0;
	if (!fx_program(slot)) {
		snprintf(out->text, sizeof(out->text), "%s", gl_log);
		free(g.fx_src[slot]);
		g.fx_src[slot] = NULL;
		return -EINVAL;
	}
	return 0;
}

static int fx_set_texture(struct req *r, int *fd)
{
	uint32_t slot = r->a[0], w = r->a[1], h = r->a[2], y0 = r->a[4], y1 = r->a[5];
	if (slot >= FX_TEXTURES || !side_ok(w) || !side_ok(h) || y0 > y1 || y1 > h)
		return -EINVAL;
	int err = shm_take(&g.fx_mem[slot], fd, (uint64_t)w * h * 4);
	if (err)
		return err;
	if (fx_current())
		return -EIO;
	int fresh = !g.fx_tex[slot] || w != g.fx_w[slot] || h != g.fx_h[slot];
	if (!g.fx_tex[slot])
		g.fx_tex[slot] = texture();
	glBindTexture(GL_TEXTURE_2D, g.fx_tex[slot]);
	GLint filter = r->a[3] & 1 ? GL_LINEAR : GL_NEAREST;
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, filter);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, filter);
	const uint8_t *px = g.fx_mem[slot].p;
	if (fresh)
		glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, px);
	else if (y1 == 0)
		glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, w, h, GL_RGBA, GL_UNSIGNED_BYTE, px);
	else if (y1 > y0)
		glTexSubImage2D(GL_TEXTURE_2D, 0, 0, y0, w, y1 - y0, GL_RGBA, GL_UNSIGNED_BYTE, px + (size_t)y0 * w * 4);
	g.fx_w[slot] = w, g.fx_h[slot] = h;
	return glGetError() == GL_NO_ERROR ? 0 : -EIO;
}

// fx_draw_with draws prog over the viewport as it is set, with the textures in reads, the canvas and
// n parameters.
static void fx_draw_with(GLuint prog, uint32_t reads, uint32_t n, const float *params)
{
	glUseProgram(prog);
	for (uint32_t i = 0; i < FX_TEXTURES; i++) {
		if (!(reads >> i & 1))
			continue;
		char name[4] = { 't', '0' + i, 0 };
		glActiveTexture(GL_TEXTURE1 + i);
		glBindTexture(GL_TEXTURE_2D, g.fx_tex[i]);
		glUniform1i(glGetUniformLocation(prog, name), 1 + i);
	}
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_2D, g.canvas_tex);
	// Smoothly between the canvas's pixels, for an effect that moves the page; at a pixel's middle
	// that is the pixel itself. A plain present reads it as it is, and says so again (draw_canvas).
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
	glUniform1i(glGetUniformLocation(prog, "canvas"), 0);
	glUniform2f(glGetUniformLocation(prog, "csize"), g.cw, g.ch);
	if (n)
		glUniform4fv(glGetUniformLocation(prog, "p"), n, params);
	quad();
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
}

// fx_draw draws the frame in hand and shows it.
static int fx_draw(void)
{
	if (panel())
		return -EIO;
	GLuint prog = fx_program(g.fx_slot);
	if (!prog || g.fx_pass[g.fx_slot])
		return -EINVAL;
	glUseProgram(prog);
	glUniform1f(glGetUniformLocation(prog, "rotated"), g.rotated);
	panel_uniforms(prog);
	fx_draw_with(prog, g.fx_reads, g.fx_n, g.fx_p);
	return show();
}

static int fx_pass_draw(struct req *r, int *fd)
{
	uint32_t slot = r->a[0], to = r->a[1], n = r->a[2], reads = r->a[3];
	if (!g.open)
		return -ENODEV;
	if (slot >= FX_PROGRAMS || !g.fx_src[slot] || !g.fx_pass[slot] || to >= FX_TEXTURES || !g.fx_tex[to] ||
	    (reads >> to & 1) || n > FX_PARAMS || reads >> FX_TEXTURES)
		return -EINVAL;
	for (uint32_t i = 0; i < FX_TEXTURES; i++)
		if ((reads >> i & 1) && !g.fx_tex[i])
			return -EINVAL;
	uint32_t w = g.fx_w[to], h = g.fx_h[to];
	uint32_t x0 = r->a[4] & 0xffff, y0 = r->a[4] >> 16, x1 = r->a[5] & 0xffff, y1 = r->a[5] >> 16;
	if (!r->a[5])
		x0 = y0 = 0, x1 = w, y1 = h;
	if (x0 >= x1 || y0 >= y1 || x1 > w || y1 > h)
		return -EINVAL;
	int err = n ? shm_take(&g.fx_params, fd, (uint64_t)n * 4 * sizeof(float)) : 0;
	if (err)
		return err;
	if (fx_current())
		return -EIO;
	GLuint prog = fx_program(slot);
	if (!prog)
		return -EINVAL;
	if (!g.fx_fbo)
		glGenFramebuffers(1, &g.fx_fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, g.fx_fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, g.fx_tex[to], 0);
	if (glCheckFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE)
		return -EIO;
	glViewport(0, 0, w, h);
	glDisable(GL_BLEND);
	// gl_FragCoord's rows are the texture's own from its first: drawn and read in the same order.
	glEnable(GL_SCISSOR_TEST);
	glScissor(x0, y0, x1 - x0, y1 - y0);
	fx_draw_with(prog, reads, n, g.fx_params.p);
	glDisable(GL_SCISSOR_TEST);
	return glGetError() == GL_NO_ERROR ? 0 : -EIO;
}

static int fx_present(struct req *r, int *fd)
{
	uint32_t flags = r->a[0], slot = r->a[1], n = r->a[2], reads = r->a[3];
	if (!g.open)
		return -ENODEV;
	if (slot >= FX_PROGRAMS || !g.fx_src[slot] || n > FX_PARAMS || reads >> FX_TEXTURES)
		return -EINVAL;
	for (uint32_t i = 0; i < FX_TEXTURES; i++)
		if ((reads >> i & 1) && !g.fx_tex[i])
			return -EINVAL;
	int err = n ? shm_take(&g.fx_params, fd, (uint64_t)n * 4 * sizeof(float)) : 0;
	if (err)
		return err;
	if (panel())
		return -EIO;
	if (!(flags & 1))
		canvas_take();
	// The numbers as they are now: echod writes the next frame's while this one is drawn.
	if (n)
		memcpy(g.fx_p, g.fx_params.p, (size_t)n * 4 * sizeof(float));
	g.fx_slot = slot, g.fx_n = n, g.fx_reads = reads;
	g.canvas_shown = 0; // the panel shows more than the canvas: the lift does not draw it again
	if (g.fb >= 0 && (flags & 2)) {
		later = fx_draw;
		return 0;
	}
	return fx_draw();
}

// ---- the framebuffer's panel ----
//
// The 1st gen Show 5's kernel is Amazon's 4.9, with no DRM: its GPU is behind Arm's kbase driver
// (which Mesa's panfrost drives through its kbase backend, tools/linux/patches/mesa) and its panel is
// /dev/fb0, three pages that echod writes and pans between when it draws on the cores. Here the GPU
// draws the panel's pixels into a picture in shared memory, a memfd given to GL as an EGL image, which
// that backend has the GPU write where it is; the finished picture is copied to the page not on show,
// about a millisecond for the Show 5's 480x960, and panned to, which waits for the panel's refresh. A
// video frame is given to GL the same
// way, so the GPU reads it where echod wrote it: echod writes the next one only once this has
// answered, with the frame on the panel. The canvas is uploaded (canvas_take says why).
//
// Should the driver not take a memfd as an image, each falls back on its own to what works anywhere:
// the frame is uploaded, and the page is read back with glReadPixels (slowly).

#define FOURCC(a, b, c, d) ((uint32_t)(a) | (uint32_t)(b) << 8 | (uint32_t)(c) << 16 | (uint32_t)(d) << 24)

// image_of is an EGL image of the w x h pixels in fd, four bytes each (B, G, R, A when bgra, else R, G,
// B, A), for the GPU to use where they are. EGL_NO_IMAGE_KHR where that cannot be: a driver that takes
// only dma-bufs, or pixels that do not fill whole pages.
static EGLImageKHR image_of(int fd, uint32_t w, uint32_t h, int bgra)
{
	const char *ext = eglQueryString(g.dpy, EGL_EXTENSIONS);
	if (fd < 0 || !create_image || !image_texture || !ext || !strstr(ext, "EGL_EXT_image_dma_buf_import") ||
	    (uint64_t)w * h * 4 % (uint64_t)sysconf(_SC_PAGESIZE))
		return EGL_NO_IMAGE_KHR;
	const EGLint attrs[] = {
		EGL_WIDTH, (EGLint)w, EGL_HEIGHT, (EGLint)h,
		EGL_LINUX_DRM_FOURCC_EXT, (EGLint)(bgra ? FOURCC('A', 'R', '2', '4') : FOURCC('A', 'B', '2', '4')),
		EGL_DMA_BUF_PLANE0_FD_EXT, fd,
		EGL_DMA_BUF_PLANE0_OFFSET_EXT, 0,
		EGL_DMA_BUF_PLANE0_PITCH_EXT, (EGLint)(w * 4),
		EGL_NONE };
	return create_image(g.dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, attrs);
}

// fb_test is TECHO5_GPU_FB_TEST=WxH:file, for tests: the file stands in for the framebuffer, a panel
// of W x H with as many pages as the file holds, bytes B, G, R, A, and showing a page is nothing
// more than having written it. With it the whole of this path runs anywhere GL does, on a software
// renderer too.
static int fb_test(const char *spec)
{
	unsigned w = 0, h = 0;
	int at = 0;
	if (sscanf(spec, "%ux%u:%n", &w, &h, &at) < 2 || !at || !side_ok(w) || !side_ok(h))
		return -EINVAL;
	int fd = open(spec + at, O_RDWR | O_CLOEXEC);
	if (fd < 0)
		return -errno;
	size_t page = (size_t)w * h * 4;
	off_t size = lseek(fd, 0, SEEK_END);
	if (size < (off_t)page) {
		close(fd);
		return -EINVAL;
	}
	void *mem = mmap(NULL, size - size % page, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (mem == MAP_FAILED) {
		close(fd);
		return -ENOMEM;
	}
	g.fb = fd, g.fb_mem = mem, g.fb_size = size - size % page, g.fb_test = 1;
	g.fb_line = w * 4, g.fb_pages = size / page, g.fb_bgra = 1;
	g.pw = w, g.ph = h;
	return 0;
}

// fb_start opens the framebuffer and maps its pages: 32 bits a pixel, bytes B, G, R, A or R, G, B, A.
static int fb_start(void)
{
	const char *test = getenv("TECHO5_GPU_FB_TEST");
	if (test)
		return fb_test(test);
	int fd = open("/dev/fb0", O_RDWR | O_CLOEXEC);
	if (fd < 0)
		return -errno;
	struct fb_var_screeninfo v;
	struct fb_fix_screeninfo f;
	if (ioctl(fd, FBIOGET_VSCREENINFO, &v) || ioctl(fd, FBIOGET_FSCREENINFO, &f)) {
		close(fd);
		return -ENODEV;
	}
	int bgra = v.red.offset == 16 && v.green.offset == 8 && v.blue.offset == 0;
	int rgba = v.red.offset == 0 && v.green.offset == 8 && v.blue.offset == 16;
	if (v.bits_per_pixel != 32 || (!bgra && !rgba) || !side_ok(v.xres) || !side_ok(v.yres) ||
	    f.line_length < v.xres * 4) {
		say("a framebuffer of %ux%u at %u bits, red at bit %u: not one this draws on", v.xres, v.yres,
		    v.bits_per_pixel, v.red.offset);
		close(fd);
		return -ENODEV;
	}
	size_t page = (size_t)f.line_length * v.yres;
	uint32_t pages = v.yres_virtual / v.yres;
	if (f.smem_len && (size_t)f.smem_len < page * pages)
		pages = f.smem_len / page;
	if (pages < 1)
		pages = 1;
	void *mem = mmap(NULL, page * pages, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (mem == MAP_FAILED) {
		close(fd);
		return -ENOMEM;
	}
	g.fb = fd, g.fb_var = v, g.fb_mem = mem, g.fb_size = page * pages;
	g.fb_line = f.line_length, g.fb_pages = pages, g.fb_bgra = bgra;
	g.fb_page = v.yoffset / v.yres < pages ? v.yoffset / v.yres : 0;
	g.pw = v.xres, g.ph = v.yres;
	return 0;
}

// fb_open makes the picture the panel's pixels are drawn into.
static int fb_open(void)
{
	size_t size = (size_t)g.pw * g.ph * 4;
	int fd = memfd_create("techo5-page", MFD_CLOEXEC);
	if (fd < 0)
		return -errno;
	if (ftruncate(fd, size)) {
		close(fd);
		return -ENOMEM;
	}
	void *p = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (p == MAP_FAILED) {
		close(fd);
		return -ENOMEM;
	}
	g.page.fd = fd, g.page.p = p, g.page.size = size;
	if (!eglMakeCurrent(g.dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, g.ctx))
		return -EIO;
	g.page_tex = texture();
	g.page_img = image_of(fd, g.pw, g.ph, g.fb_bgra);
	if (g.page_img != EGL_NO_IMAGE_KHR) {
		image_texture(GL_TEXTURE_2D, g.page_img);
		if (glGetError() != GL_NO_ERROR) {
			destroy_image(g.dpy, g.page_img);
			g.page_img = EGL_NO_IMAGE_KHR;
		}
	}
	if (g.page_img == EGL_NO_IMAGE_KHR) {
		say("the GPU cannot draw into shared memory here: every frame is read back");
		glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, g.pw, g.ph, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
	}
	glGenFramebuffers(1, &g.page_fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, g.page_fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, g.page_tex, 0);
	return glCheckFramebufferStatus(GL_FRAMEBUFFER) == GL_FRAMEBUFFER_COMPLETE ? 0 : -EINVAL;
}

// fb_show puts the drawn picture on the page not on show, and pans to it.
static int fb_show(void)
{
	double t0 = debug ? now_ms() : 0;
	if (g.page_img != EGL_NO_IMAGE_KHR) {
		glFinish(); // the picture is whole in its memory once the GPU is done
	} else {
		glReadPixels(0, 0, g.pw, g.ph, GL_RGBA, GL_UNSIGNED_BYTE, g.page.p);
		if (g.fb_bgra)
			for (size_t i = 0; i < g.page.size; i += 4) {
				uint8_t *q = (uint8_t *)g.page.p + i, t = q[0];
				q[0] = q[2], q[2] = t;
			}
	}
	if (glGetError() != GL_NO_ERROR)
		return -EIO;
	double t1 = debug ? now_ms() : 0;
	uint32_t next = g.fb_pages > 1 ? (g.fb_page + 1) % g.fb_pages : 0;
	uint8_t *dst = g.fb_mem + (size_t)next * g.ph * g.fb_line;
	const uint8_t *src = g.page.p;
	if (g.fb_line == g.pw * 4)
		memcpy(dst, src, g.page.size);
	else
		for (uint32_t y = 0; y < g.ph; y++)
			memcpy(dst + (size_t)y * g.fb_line, src + (size_t)y * g.pw * 4, (size_t)g.pw * 4);
	double t2 = debug ? now_ms() : 0;
	struct fb_var_screeninfo v = g.fb_var;
	v.xoffset = 0, v.yoffset = next * g.ph;
	if (!g.fb_test && ioctl(g.fb, FBIOPAN_DISPLAY, &v))
		return -errno;
	g.fb_page = next;
	if (debug)
		say("show: the GPU %.2f ms, copy to page %u %.2f ms, pan %.2f ms", t1 - t0, next, t2 - t1, now_ms() - t2);
	return 0;
}

// ---- color ----

// color_props finds the CRTC's CTM and GAMMA_LUT, and puts back to the identity either one that a
// process before this one left set: a tint must not outlive the echod that asked for it.
static void color_props(void)
{
	drmModeObjectProperties *pr = drmModeObjectGetProperties(g.drm, g.crtc, DRM_MODE_OBJECT_CRTC);
	uint64_t ctm = 0, lut = 0;
	for (uint32_t i = 0; pr && i < pr->count_props; i++) {
		drmModePropertyRes *p = drmModeGetProperty(g.drm, pr->props[i]);
		if (!p)
			continue;
		if (!strcmp(p->name, "CTM"))
			g.ctm_prop = p->prop_id, ctm = pr->prop_values[i];
		else if (!strcmp(p->name, "GAMMA_LUT"))
			g.lut_prop = p->prop_id, lut = pr->prop_values[i];
		else if (!strcmp(p->name, "GAMMA_LUT_SIZE"))
			g.lut_size = pr->prop_values[i];
		drmModeFreeProperty(p);
	}
	drmModeFreeObjectProperties(pr);
	// An MT8163 device tree from before its CCORR's fallback was mediatek,mt8183-disp-ccorr has the
	// driver write 1.0 as 0x800, which the block reads as -2.0: no matrix there rather than a wrong one.
	char compat[128] = { 0 };
	int fd = open("/proc/device-tree/soc/ccorr@1400d000/compatible", O_RDONLY | O_CLOEXEC);
	if (fd >= 0) {
		ssize_t n = read(fd, compat, sizeof(compat) - 1);
		close(fd);
		for (ssize_t i = 0; i < n; i++)
			if (!compat[i])
				compat[i] = ' ';
		if (strstr(compat, "mt8163-disp-ccorr") && strstr(compat, "mt8192-disp-ccorr")) {
			say("the CCORR's device tree gives the wrong coefficient scale; no color matrix");
			g.ctm_prop = 0, ctm = 0;
		}
	}
	if (!g.lut_prop || g.lut_size < 2)
		g.lut_prop = 0, g.lut_size = 0;
	g.ctm_set = ctm != 0, g.lut_set = lut != 0;
}

// set_blob sets prop to a blob of data, a commit that waits for the vblank it takes effect at.
static int set_blob(uint32_t prop, const void *data, size_t size)
{
	uint32_t blob;
	if (drmModeCreatePropertyBlob(g.drm, data, size, &blob))
		return -errno;
	int err = drmModeObjectSetProperty(g.drm, g.crtc, DRM_MODE_OBJECT_CRTC, prop, blob) ? -errno : 0;
	drmModeDestroyPropertyBlob(g.drm, blob); // the CRTC's state keeps its own reference
	return err;
}

// set_color puts c (or the identity, for what is not given) into the CRTC's color blocks. Mainline
// mediatek-drm leaves a block as it was when its property is cleared, so the identity is set, not
// nothing: the MT8183's CCORR at the identity passes pixels through exactly (1.0 is 1024 there), and
// its GAMMA stays in relay until a curve is first set.
static int set_color(uint32_t flags, const struct color *c)
{
	int err = 0;
	if (g.ctm_prop && ((flags & 1) || g.ctm_set)) {
		struct drm_color_ctm ctm;
		for (int i = 0; i < 9; i++) {
			int64_t v = (flags & 1) ? c->m[i] : (i % 4 == 0 ? 65536 : 0);
			uint64_t mag = (uint64_t)(v < 0 ? -v : v) << 16; // 16.16 to S31.32, sign and magnitude
			ctm.matrix[i] = v < 0 ? mag | 1ULL << 63 : mag;
		}
		err = set_blob(g.ctm_prop, &ctm, sizeof(ctm));
		g.ctm_set = 1;
	}
	if (!err && g.lut_prop && ((flags & 2) || g.lut_set)) {
		struct drm_color_lut *lut = calloc(g.lut_size, sizeof(*lut));
		if (!lut)
			return -ENOMEM;
		for (uint32_t k = 0; k < g.lut_size; k++) {
			uint32_t x = k * 255 * 256 / (g.lut_size - 1); // the input, 8.8 fixed point
			uint32_t i = x >> 8, f = x & 255, j = i < 255 ? i + 1 : 255;
			uint16_t *ch[3] = { &lut[k].red, &lut[k].green, &lut[k].blue };
			for (int n = 0; n < 3; n++)
				*ch[n] = (flags & 2) ? (uint16_t)((c->curve[n][i] * (256 - f) + c->curve[n][j] * f) >> 8)
						     : (uint16_t)(x * 65535 / (255 * 256));
		}
		err = set_blob(g.lut_prop, lut, g.lut_size * sizeof(*lut));
		free(lut);
		g.lut_set = 1;
	}
	return err;
}

static int color(struct req *r, int *fd, struct rep *out)
{
	if (!g.open)
		return -ENODEV;
	uint32_t flags = r->a[0] & 3;
	int err = 0;
	if (flags && (err = shm_take(&g.color, fd, sizeof(struct color))))
		return err;
	if (g.fb >= 0) {
		// No color blocks behind the framebuffer: the matrix goes into the shaders that draw the
		// panel's pixels, and what is on the panel is drawn again with it, as a block would have
		// changed it without a new frame.
		const struct color *c = g.color.p;
		g.ctm_on = 0;
		for (int i = 0; (flags & 1) && i < 9; i++) {
			g.ctm[i % 3 * 3 + i / 3] = c->m[i] / 65536.0f;
			if (c->m[i] != (i % 4 == 0 ? 65536 : 0))
				g.ctm_on = 1;
		}
		out->a[0] = 0, out->a[1] = 1;
		return g.canvas_shown ? redraw() : 0;
	}
	err = set_color(flags, g.color.p);
	out->a[0] = g.lut_size;
	out->a[1] = (g.ctm_prop ? 1 : 0) | (g.lut_prop ? 2 : 0);
	return err;
}

// ---- the camera ----

static int target(GLuint *tex, GLuint *fbo, uint32_t w, uint32_t h)
{
	if (!*tex) {
		*tex = texture();
		glGenFramebuffers(1, fbo);
	}
	glBindTexture(GL_TEXTURE_2D, *tex);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
	glBindFramebuffer(GL_FRAMEBUFFER, *fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, *tex, 0);
	return glCheckFramebufferStatus(GL_FRAMEBUFFER) == GL_FRAMEBUFFER_COMPLETE ? 0 : -EINVAL;
}

// dma_target makes the linear buffer a YUYV picture of w x h is drawn into, w/2 x h RGBA texels
// whose bytes in memory are R, G, B, A (DRM's ABGR8888), and exports it as a dmabuf.
static int dma_target(uint32_t w, uint32_t h, int *fd, uint32_t *stride)
{
	*fd = -1;
	if (g.dma_bo && g.dma_w == w && g.dma_h == h)
		return 0;
	if (g.dma_bo) {
		glDeleteFramebuffers(1, &g.dma_fbo);
		glDeleteTextures(1, &g.dma_tex);
		destroy_image(g.dpy, g.dma_img);
		gbm_bo_destroy(g.dma_bo);
		g.dma_bo = NULL;
	}
	struct gbm_bo *bo = gbm_bo_create(g.gbm, w / 2, h, GBM_FORMAT_ABGR8888, GBM_BO_USE_RENDERING | GBM_BO_USE_LINEAR);
	if (!bo)
		return -ENOMEM;
	int dfd = gbm_bo_get_fd(bo);
	EGLint attrs[] = {
		EGL_WIDTH, (EGLint)(w / 2), EGL_HEIGHT, (EGLint)h,
		EGL_LINUX_DRM_FOURCC_EXT, GBM_FORMAT_ABGR8888,
		EGL_DMA_BUF_PLANE0_FD_EXT, dfd,
		EGL_DMA_BUF_PLANE0_OFFSET_EXT, 0,
		EGL_DMA_BUF_PLANE0_PITCH_EXT, (EGLint)gbm_bo_get_stride(bo),
		EGL_NONE };
	if (!create_image || !image_texture) {
		close(dfd);
		gbm_bo_destroy(bo);
		return -ENOSYS;
	}
	EGLImageKHR img = create_image(g.dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, attrs);
	if (img == EGL_NO_IMAGE_KHR) {
		close(dfd);
		gbm_bo_destroy(bo);
		return -EINVAL;
	}
	glGenTextures(1, &g.dma_tex);
	glBindTexture(GL_TEXTURE_2D, g.dma_tex);
	image_texture(GL_TEXTURE_2D, img);
	glGenFramebuffers(1, &g.dma_fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, g.dma_fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, g.dma_tex, 0);
	if (glCheckFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE) {
		glBindFramebuffer(GL_FRAMEBUFFER, 0);
		glDeleteFramebuffers(1, &g.dma_fbo);
		glDeleteTextures(1, &g.dma_tex);
		g.dma_fbo = g.dma_tex = 0;
		destroy_image(g.dpy, img);
		gbm_bo_destroy(bo);
		close(dfd);
		return -EINVAL;
	}
	g.dma_bo = bo, g.dma_img = img, g.dma_w = w, g.dma_h = h;
	*fd = dfd;
	*stride = gbm_bo_get_stride(bo);
	return 0;
}

static int develop(struct req *r, int fds[3], struct rep *out, int *out_fd)
{
	uint32_t w = r->a[0], h = r->a[1], bpl = r->a[2], bluefirst = r->a[3], mode = r->a[4];
	int err;
	if (mode > 3 || !side_ok(w) || !side_ok(h) || bpl == 0 || bpl > 2 * MAX_SIDE ||
	    (uint64_t)bpl * 4 < (uint64_t)w * 5 || w % 8 || h % 2)
		return -EINVAL;
	uint64_t need = mode == 0 ? (uint64_t)w * h * 4 : mode == 3 ? (uint64_t)w * h * 3 / 2 : (uint64_t)w * h * 2;
	if ((err = shm_take(&g.raw, &fds[0], (uint64_t)bpl * h)) || (err = shm_take(&g.tables, &fds[1], 3 * 2048)))
		return err;
	// The out buffer is taken whenever it is passed, used or not, so a later read-back finds it mapped.
	if ((fds[2] >= 0 || mode != 2) && (err = shm_take(&g.out, &fds[2], mode == 2 ? 1 : need)))
		return err;
	if (!eglMakeCurrent(g.dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, g.ctx))
		return -EIO;
	if (!g.raw_tex) {
		g.raw_tex = texture();
		g.tab_tex = texture();
	}
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_2D, g.raw_tex);
	if (w != g.dw || h != g.dh || bpl != g.dbpl) {
		glTexImage2D(GL_TEXTURE_2D, 0, GL_LUMINANCE, bpl, h, 0, GL_LUMINANCE, GL_UNSIGNED_BYTE, NULL);
		if ((err = target(&g.smp_tex, &g.smp_fbo, w, h)) || (err = target(&g.rgb_tex, &g.rgb_fbo, w, h)) ||
		    (err = target(&g.yuyv_tex, &g.yuyv_fbo, w / 2, h)))
			return err;
		g.dw = w, g.dh = h, g.dbpl = bpl;
		glActiveTexture(GL_TEXTURE0);
		glBindTexture(GL_TEXTURE_2D, g.raw_tex);
	}
	double t0 = debug ? now_ms() : 0;
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, bpl, h, GL_LUMINANCE, GL_UNSIGNED_BYTE, g.raw.p);
	glActiveTexture(GL_TEXTURE1);
	glBindTexture(GL_TEXTURE_2D, g.tab_tex);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_LUMINANCE, 2048, 3, 0, GL_LUMINANCE, GL_UNSIGNED_BYTE, g.tables.p);

	glDisable(GL_BLEND);
	glBindFramebuffer(GL_FRAMEBUFFER, g.smp_fbo);
	glViewport(0, 0, w, h);
	glUseProgram(g.p_unpack);
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_2D, g.raw_tex);
	glUniform1i(glGetUniformLocation(g.p_unpack, "raw"), 0);
	glUniform2f(glGetUniformLocation(g.p_unpack, "size"), w, h);
	glUniform1f(glGetUniformLocation(g.p_unpack, "bpl"), bpl);
	quad();

	glBindFramebuffer(GL_FRAMEBUFFER, g.rgb_fbo);
	glUseProgram(g.p_develop);
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_2D, g.smp_tex);
	glUniform1i(glGetUniformLocation(g.p_develop, "samples"), 0);
	glUniform1i(glGetUniformLocation(g.p_develop, "tables"), 1);
	glUniform2f(glGetUniformLocation(g.p_develop, "size"), w, h);
	glUniform1f(glGetUniformLocation(g.p_develop, "bluefirst"), bluefirst);
	quad();
	double t1 = 0, t2 = 0;
	if (debug) {
		t1 = now_ms();
		glFinish();
		t2 = now_ms();
	}
	if (mode == 1 || mode == 2) {
		if (mode == 2) {
			uint32_t stride = 0;
			if ((err = dma_target(w, h, out_fd, &stride)))
				return err;
			if (*out_fd >= 0)
				out->a[0] = 1, out->a[1] = stride;
			glBindFramebuffer(GL_FRAMEBUFFER, g.dma_fbo);
		} else {
			glBindFramebuffer(GL_FRAMEBUFFER, g.yuyv_fbo);
		}
		glViewport(0, 0, w / 2, h);
		glUseProgram(g.p_yuyv);
		glActiveTexture(GL_TEXTURE0);
		glBindTexture(GL_TEXTURE_2D, g.rgb_tex);
		glUniform1i(glGetUniformLocation(g.p_yuyv, "rgb"), 0);
		glUniform2f(glGetUniformLocation(g.p_yuyv, "size"), w, h);
		quad();
		if (mode == 1)
			glReadPixels(0, 0, w / 2, h, GL_RGBA, GL_UNSIGNED_BYTE, g.out.p);
		else
			glFinish(); // the encoder reads it as soon as the reply is out
	} else if (mode == 3) {
		uint8_t *dst = g.out.p;
		for (int pl = 0; pl < 3; pl++) {
			uint32_t pw = pl ? w / 2 : w, ph = pl ? h / 2 : h;
			if (!g.plane_tex[pl] || w != g.plw || h != g.plh) {
				if ((err = target(&g.plane_tex[pl], &g.plane_fbo[pl], pw / 4, ph)))
					return err;
				if (pl == 2)
					g.plw = w, g.plh = h;
			}
			glBindFramebuffer(GL_FRAMEBUFFER, g.plane_fbo[pl]);
			glViewport(0, 0, pw / 4, ph);
			glUseProgram(g.p_planar);
			glActiveTexture(GL_TEXTURE0);
			glBindTexture(GL_TEXTURE_2D, g.rgb_tex);
			glUniform1i(glGetUniformLocation(g.p_planar, "rgb"), 0);
			glUniform2f(glGetUniformLocation(g.p_planar, "size"), w, h);
			glUniform1f(glGetUniformLocation(g.p_planar, "plane"), pl);
			quad();
			glReadPixels(0, 0, pw / 4, ph, GL_RGBA, GL_UNSIGNED_BYTE, dst);
			dst += (size_t)pw * ph;
		}
	} else {
		glReadPixels(0, 0, w, h, GL_RGBA, GL_UNSIGNED_BYTE, g.out.p);
	}
	if (debug)
		say("develop %ux%u mode %u: upload+submit %.1f ms, shader %.1f ms, convert+out %.1f ms",
		    w, h, mode, t1 - t0, t2 - t1, now_ms() - t2);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	return glGetError() == GL_NO_ERROR ? 0 : -EIO;
}

// ---- the socket ----

static int receive(int sock, struct req *r, int fds[3])
{
	char cbuf[CMSG_SPACE(3 * sizeof(int))];
	struct iovec iov = { r, sizeof(*r) };
	struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = cbuf, .msg_controllen = sizeof(cbuf) };
	fds[0] = fds[1] = fds[2] = -1;
	ssize_t n = recvmsg(sock, &m, MSG_CMSG_CLOEXEC);
	if (n <= 0)
		return n == 0 ? 0 : -1;
	// Up to three descriptors are kept; any more, or any with a message of the wrong size, are closed.
	int kept = 0;
	for (struct cmsghdr *c = CMSG_FIRSTHDR(&m); c; c = CMSG_NXTHDR(&m, c))
		if (c->cmsg_level == SOL_SOCKET && c->cmsg_type == SCM_RIGHTS) {
			int k = (c->cmsg_len - CMSG_LEN(0)) / sizeof(int);
			int got[8];
			memcpy(got, CMSG_DATA(c), (k > 8 ? 8 : k) * sizeof(int));
			for (int i = 0; i < k && i < 8; i++) {
				if (n == sizeof(*r) && kept < 3)
					fds[kept++] = got[i];
				else
					close(got[i]);
			}
		}
	return n == sizeof(*r) ? 1 : -1;
}

// reset_color is `techo5-gpu --reset-color`: the CRTC's color blocks put back to the identity, and
// nothing else (no GL). The kernel keeps a CTM and GAMMA_LUT set on the CRTC after the process that set
// them is gone, so a helper that died with the night's tint on left it on the panel, under the
// framebuffer echod draws on instead, which tints on the cores as well. echod runs this once the
// helper has exited.
static int reset_color(void)
{
	g.drm = open_card();
	if (g.drm < 0) // the framebuffer's panel has no color blocks to keep anything
		return getenv("TECHO5_GPU_FB_TEST") || !access("/dev/fb0", F_OK) ? 0 : 1;
	drmModeRes *res = drmModeGetResources(g.drm);
	if (!res)
		return 1;
	for (int c = 0; c < res->count_connectors && !g.crtc; c++) {
		drmModeConnector *conn = drmModeGetConnector(g.drm, res->connectors[c]);
		if (conn && conn->connection == DRM_MODE_CONNECTED && conn->count_modes) {
			drmModeEncoder *enc = conn->encoder_id ? drmModeGetEncoder(g.drm, conn->encoder_id) : NULL;
			g.crtc = enc && enc->crtc_id ? enc->crtc_id : res->count_crtcs > 0 ? res->crtcs[0] : 0;
			drmModeFreeEncoder(enc);
		}
		drmModeFreeConnector(conn);
	}
	drmModeFreeResources(res);
	if (!g.crtc)
		return 1;
	color_props();
	if (!g.ctm_set && !g.lut_set)
		return 0;
	int err = set_color(0, NULL);
	if (err)
		say("could not put the panel's color back: %s", strerror(-err));
	return err ? 1 : 0;
}

int main(int argc, char **argv)
{
	if (argc > 1 && !strcmp(argv[1], "--reset-color"))
		return reset_color();
	int sock = 3;
	debug = getenv("TECHO5_GPU_DEBUG") != NULL;
	int err = gl_start(), deferred = 0;
	struct req r;
	int fds[3];
	for (;;) {
		int got = receive(sock, &r, fds);
		if (got <= 0)
			break;
		struct rep out = { 0 };
		int out_fd = -1;
		if (err)
			out.status = err;
		else if (deferred)
			// The frame presented early before this request did not reach the panel: said here,
			// with nothing of this request done.
			out.status = deferred, deferred = 0;
		else
			switch (r.op) {
			case OP_HELLO:
				snprintf(out.text, sizeof(out.text), "%s", g.gpu);
				break;
			case OP_DISPLAY_OPEN:
				out.status = display_open(&r, &fds[0], &out);
				break;
			case OP_PRESENT:
				out.status = present(&r, &out);
				break;
			case OP_PRESENT_FRAME:
				out.status = present_frame(&r, &fds[0]);
				break;
			case OP_DEVELOP:
				out.status = develop(&r, fds, &out, &out_fd);
				break;
			case OP_BLANK:
				out.status = blank(r.a[0]);
				break;
			case OP_COLOR:
				out.status = color(&r, &fds[0], &out);
				break;
			case OP_LIFT:
				out.status = lift(&r);
				break;
			case OP_SYNC:
				break;
			case OP_FX_PROGRAM:
				out.status = fx_set_program(&r, &fds[0], &out);
				break;
			case OP_FX_TEXTURE:
				out.status = fx_set_texture(&r, &fds[0]);
				break;
			case OP_FX_PRESENT:
				out.status = fx_present(&r, &fds[0]);
				break;
			case OP_FX_PASS:
				out.status = fx_pass_draw(&r, &fds[0]);
				break;
			default:
				out.status = -ENOSYS;
			}
		// Whatever was passed and not taken (shm_take sets what it takes to -1) is closed: a request
		// that uses no buffer, or was refused, must not leave descriptors behind.
		for (int i = 0; i < 3; i++)
			if (fds[i] >= 0)
				close(fds[i]);
		char cbuf[CMSG_SPACE(sizeof(int))];
		struct iovec iov = { &out, sizeof(out) };
		struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1 };
		if (out_fd >= 0) {
			m.msg_control = cbuf, m.msg_controllen = sizeof(cbuf);
			struct cmsghdr *c = CMSG_FIRSTHDR(&m);
			c->cmsg_level = SOL_SOCKET, c->cmsg_type = SCM_RIGHTS, c->cmsg_len = CMSG_LEN(sizeof(int));
			memcpy(CMSG_DATA(c), &out_fd, sizeof(int));
		}
		ssize_t sent = sendmsg(sock, &m, 0);
		if (out_fd >= 0)
			close(out_fd);
		if (sent != sizeof(out))
			break;
		if (later) {
			deferred = later();
			later = NULL;
		}
	}
	return 0;
}
