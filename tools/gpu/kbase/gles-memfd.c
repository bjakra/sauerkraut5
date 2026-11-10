// SPDX-License-Identifier: MIT
//
// gles-memfd: the screen's path on a GPU behind kbase, without the screen. Two memfds stand for
// echod's canvas (landscape, R G B A bytes) and for a page of the framebuffer (portrait, B G R A);
// both are given to GL as EGL images, so the GPU reads the one and draws into the other where they
// are, with nothing uploaded or read back. Each frame the canvas gets a new picture, the GPU turns it
// onto the page the way echod's rotate does, and the page is compared with the same turn done here.
//
//   gles-memfd [-n frames] [-w canvas width] [-h canvas height] [out.ppm]

#define _GNU_SOURCE
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

#define FOURCC(a, b, c, d) ((uint32_t)(a) | (uint32_t)(b) << 8 | (uint32_t)(c) << 16 | (uint32_t)(d) << 24)
#define FORMAT_ABGR8888 FOURCC('A', 'B', '2', '4') // bytes R G B A
#define FORMAT_ARGB8888 FOURCC('A', 'R', '2', '4') // bytes B G R A

static double now_ms(void)
{
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e3 + t.tv_nsec / 1e6;
}

#define die(...) do { fprintf(stderr, "gles-memfd: " __VA_ARGS__); fprintf(stderr, " (EGL 0x%x, GL 0x%x)\n", eglGetError(), glGetError()); exit(1); } while (0)

static GLuint shader(GLenum type, const char *src)
{
	GLuint s = glCreateShader(type);
	GLint ok = 0;
	glShaderSource(s, 1, &src, NULL);
	glCompileShader(s);
	glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
	if (!ok) {
		char log[1024];
		glGetShaderInfoLog(s, sizeof log, NULL, log);
		die("shader: %s", log);
	}
	return s;
}

struct shared {
	int fd;
	uint8_t *p;
	size_t size;
	EGLImageKHR img;
	GLuint tex;
};

static struct shared share(EGLDisplay dpy, int w, int h, uint32_t format)
{
	struct shared s = {.size = (size_t)w * h * 4};
	s.fd = memfd_create("gles-memfd", MFD_CLOEXEC);
	if (s.fd < 0 || ftruncate(s.fd, s.size))
		die("memfd");
	s.p = mmap(NULL, s.size, PROT_READ | PROT_WRITE, MAP_SHARED, s.fd, 0);
	if (s.p == MAP_FAILED)
		die("mmap");
	PFNEGLCREATEIMAGEKHRPROC create_image = (void *)eglGetProcAddress("eglCreateImageKHR");
	PFNGLEGLIMAGETARGETTEXTURE2DOESPROC image_texture = (void *)eglGetProcAddress("glEGLImageTargetTexture2DOES");
	const EGLint attrs[] = {
		EGL_WIDTH, w, EGL_HEIGHT, h, EGL_LINUX_DRM_FOURCC_EXT, (EGLint)format,
		EGL_DMA_BUF_PLANE0_FD_EXT, s.fd, EGL_DMA_BUF_PLANE0_OFFSET_EXT, 0, EGL_DMA_BUF_PLANE0_PITCH_EXT, w * 4,
		EGL_NONE,
	};
	s.img = create_image(dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, attrs);
	if (s.img == EGL_NO_IMAGE_KHR)
		die("the memfd was not taken as an EGL image");
	glGenTextures(1, &s.tex);
	glBindTexture(GL_TEXTURE_2D, s.tex);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
	image_texture(GL_TEXTURE_2D, s.img);
	if (glGetError() != GL_NO_ERROR)
		die("the EGL image was not taken as a texture");
	return s;
}

// paint is frame f's picture: every pixel different from its neighbors and from the frame before.
static void paint(uint8_t *p, int w, int h, int f)
{
	for (int y = 0; y < h; y++)
		for (int x = 0; x < w; x++) {
			uint8_t *q = p + ((size_t)y * w + x) * 4;
			q[0] = x + f * 3;
			q[1] = y + f * 5;
			q[2] = (x >> 4) * 16 + (y >> 4) + f * 7;
			q[3] = 255;
		}
}

int main(int argc, char **argv)
{
	int cw = 960, ch = 480, frames = 60;
	const char *out = NULL;
	for (int i = 1; i < argc; i++) {
		if (!strcmp(argv[i], "-n") && i + 1 < argc)
			frames = atoi(argv[++i]);
		else if (!strcmp(argv[i], "-w") && i + 1 < argc)
			cw = atoi(argv[++i]);
		else if (!strcmp(argv[i], "-h") && i + 1 < argc)
			ch = atoi(argv[++i]);
		else
			out = argv[i];
	}
	int pw = ch, ph = cw; // the panel is the canvas turned

	EGLDisplay dpy = eglGetPlatformDisplay(EGL_PLATFORM_SURFACELESS_MESA, EGL_DEFAULT_DISPLAY, NULL);
	if (dpy == EGL_NO_DISPLAY || !eglInitialize(dpy, NULL, NULL))
		die("no EGL display");
	if (!strstr(eglQueryString(dpy, EGL_EXTENSIONS), "EGL_EXT_image_dma_buf_import"))
		die("no EGL_EXT_image_dma_buf_import");
	eglBindAPI(EGL_OPENGL_ES_API);
	EGLConfig cfg;
	EGLint n = 0;
	static const EGLint cfg_attrs[] = {EGL_SURFACE_TYPE, EGL_PBUFFER_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT, EGL_NONE};
	if (!eglChooseConfig(dpy, cfg_attrs, &cfg, 1, &n) || n < 1)
		die("no EGL config");
	static const EGLint ctx_attrs[] = {EGL_CONTEXT_CLIENT_VERSION, 2, EGL_NONE};
	EGLContext ctx = eglCreateContext(dpy, cfg, EGL_NO_CONTEXT, ctx_attrs);
	if (ctx == EGL_NO_CONTEXT || !eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, ctx))
		die("no GLES 2 context");
	printf("renderer %s\n", glGetString(GL_RENDERER));

	struct shared canvas = share(dpy, cw, ch, FORMAT_ABGR8888);
	struct shared page = share(dpy, pw, ph, FORMAT_ARGB8888);
	GLuint fbo;
	glGenFramebuffers(1, &fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, page.tex, 0);
	if (glCheckFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE)
		die("the page is not a complete framebuffer");

	// The page's rows are top-down in memory, and GL draws row 0 of a framebuffer first: with uv.y
	// taken as it comes, memory row r is GL's y = r, so the picture is drawn in memory order and the
	// panel row is uv.y * panelh with no flip. Panel (px, py) shows canvas (py, panelw - 1 - px).
	GLuint prog = glCreateProgram();
	glAttachShader(prog, shader(GL_VERTEX_SHADER,
				    "attribute vec2 pos; varying highp vec2 uv;\n"
				    "void main() { uv = pos * 0.5 + 0.5; gl_Position = vec4(pos, 0.0, 1.0); }\n"));
	glAttachShader(prog, shader(GL_FRAGMENT_SHADER,
				    "precision highp float; varying highp vec2 uv; uniform sampler2D canvas;\n"
				    "uniform vec2 csize; uniform vec2 psize;\n"
				    "void main() {\n"
				    "  vec2 p = uv * psize;\n"
				    "  vec2 c = vec2(p.y, psize.x - p.x);\n"
				    "  gl_FragColor = texture2D(canvas, c / csize);\n"
				    "}\n"));
	glBindAttribLocation(prog, 0, "pos");
	glLinkProgram(prog);
	GLint ok = 0;
	glGetProgramiv(prog, GL_LINK_STATUS, &ok);
	if (!ok)
		die("link");
	glUseProgram(prog);
	glUniform1i(glGetUniformLocation(prog, "canvas"), 0);
	glUniform2f(glGetUniformLocation(prog, "csize"), cw, ch);
	glUniform2f(glGetUniformLocation(prog, "psize"), pw, ph);
	static const GLfloat quad[] = {-1, -1, 1, -1, -1, 1, 1, 1};
	glVertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 0, quad);
	glEnableVertexAttribArray(0);
	glViewport(0, 0, pw, ph);
	glBindTexture(GL_TEXTURE_2D, canvas.tex);

	uint8_t *want = malloc(page.size);
	double sum = 0, worst = 0, best = 1e9;
	long wrong_frames = 0, wrong_pixels = 0;
	int max_diff = 0;
	for (int f = 0; f < frames; f++) {
		paint(canvas.p, cw, ch, f);
		double a = now_ms();
		glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
		glFinish();
		double d = now_ms() - a;
		sum += d;
		if (d > worst)
			worst = d;
		if (d < best)
			best = d;
		long bad = 0;
		for (int py = 0; py < ph; py++)
			for (int px = 0; px < pw; px++) {
				const uint8_t *c = canvas.p + ((size_t)(pw - 1 - px) * cw + py) * 4;
				const uint8_t *g = page.p + ((size_t)py * pw + px) * 4;
				int diff = abs(g[0] - c[2]) | abs(g[1] - c[1]) | abs(g[2] - c[0]);
				if (diff) {
					bad++;
					if (diff > max_diff)
						max_diff = diff;
				}
			}
		if (bad) {
			wrong_frames++;
			wrong_pixels += bad;
		}
	}
	printf("%d frame(s), canvas %dx%d onto a %dx%d page: draw+finish best %.2f mean %.2f worst %.2f ms\n", frames, cw, ch, pw, ph,
	       best, sum / frames, worst);
	printf("compared with the turn done on the CPU: %ld frame(s) differ, %ld pixel(s), largest difference %d; GL error 0x%x\n",
	       wrong_frames, wrong_pixels, max_diff, glGetError());
	if (out) {
		FILE *fp = fopen(out, "wb");
		if (!fp)
			die("cannot write %s", out);
		fprintf(fp, "P6\n%d %d\n255\n", pw, ph);
		for (size_t i = 0; i < (size_t)pw * ph; i++) {
			const uint8_t px[3] = {page.p[i * 4 + 2], page.p[i * 4 + 1], page.p[i * 4]};
			fwrite(px, 1, 3, fp);
		}
		fclose(fp);
	}
	(void)want;
	return wrong_frames != 0;
}
