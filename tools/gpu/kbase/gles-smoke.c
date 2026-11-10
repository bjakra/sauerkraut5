// SPDX-License-Identifier: MIT
//
// gles-smoke: the smallest whole trip through the GL stack with no window system: an EGL surfaceless
// context, a shader, a triangle drawn into a texture, the pixels read back and written as a PPM
// picture. It prints what GL calls itself, so a run shows which driver drew.
//
//   gles-smoke [-s size] [-n frames] [out.ppm]

#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static double now_ms(void)
{
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e3 + t.tv_nsec / 1e6;
}

#define die(...) do { fprintf(stderr, "gles-smoke: " __VA_ARGS__); fprintf(stderr, " (EGL 0x%x, GL 0x%x)\n", eglGetError(), glGetError()); exit(1); } while (0)

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

int main(int argc, char **argv)
{
	int size = 256, frames = 1;
	const char *out = NULL;
	for (int i = 1; i < argc; i++) {
		if (!strcmp(argv[i], "-s") && i + 1 < argc)
			size = atoi(argv[++i]);
		else if (!strcmp(argv[i], "-n") && i + 1 < argc)
			frames = atoi(argv[++i]);
		else
			out = argv[i];
	}

	double t0 = now_ms();
	EGLDisplay dpy = eglGetPlatformDisplay(EGL_PLATFORM_SURFACELESS_MESA, EGL_DEFAULT_DISPLAY, NULL);
	if (dpy == EGL_NO_DISPLAY || !eglInitialize(dpy, NULL, NULL))
		die("no EGL display");
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
	printf("vendor   %s\nrenderer %s\nversion  %s\nglsl     %s\n", glGetString(GL_VENDOR), glGetString(GL_RENDERER),
	       glGetString(GL_VERSION), glGetString(GL_SHADING_LANGUAGE_VERSION));
	double t1 = now_ms();

	GLuint tex, fbo;
	glGenTextures(1, &tex);
	glBindTexture(GL_TEXTURE_2D, tex);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, size, size, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glGenFramebuffers(1, &fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);
	if (glCheckFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE)
		die("framebuffer incomplete");

	GLuint prog = glCreateProgram();
	glAttachShader(prog, shader(GL_VERTEX_SHADER,
				    "attribute vec2 pos; attribute vec3 col; varying vec3 v;\n"
				    "void main() { v = col; gl_Position = vec4(pos, 0.0, 1.0); }\n"));
	glAttachShader(prog, shader(GL_FRAGMENT_SHADER,
				    "precision mediump float; varying vec3 v; uniform float gain;\n"
				    "void main() { gl_FragColor = vec4(v * gain, 1.0); }\n"));
	glBindAttribLocation(prog, 0, "pos");
	glBindAttribLocation(prog, 1, "col");
	glLinkProgram(prog);
	GLint ok = 0;
	glGetProgramiv(prog, GL_LINK_STATUS, &ok);
	if (!ok)
		die("link");
	glUseProgram(prog);
	static const GLfloat verts[] = {
		-0.8f, -0.8f, 1, 0, 0,
		0.8f, -0.8f, 0, 1, 0,
		0.0f, 0.8f, 0, 0, 1,
	};
	glVertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 5 * sizeof(GLfloat), verts);
	glVertexAttribPointer(1, 3, GL_FLOAT, GL_FALSE, 5 * sizeof(GLfloat), verts + 2);
	glEnableVertexAttribArray(0);
	glEnableVertexAttribArray(1);
	glViewport(0, 0, size, size);
	double t2 = now_ms();

	unsigned char *px = malloc((size_t)size * size * 4);
	double worst = 0, sum = 0;
	for (int f = 0; f < frames; f++) {
		double a = now_ms();
		glClearColor(0.1f, 0.2f, 0.3f, 1.0f);
		glClear(GL_COLOR_BUFFER_BIT);
		glUniform1f(glGetUniformLocation(prog, "gain"), 1.0f);
		glDrawArrays(GL_TRIANGLES, 0, 3);
		glReadPixels(0, 0, size, size, GL_RGBA, GL_UNSIGNED_BYTE, px);
		double d = now_ms() - a;
		sum += d;
		if (d > worst)
			worst = d;
	}
	GLenum err = glGetError();
	printf("setup: egl %.1f ms, program %.1f ms; %d frame(s) of %dx%d: mean %.2f ms, worst %.2f ms; GL error 0x%x\n", t1 - t0,
	       t2 - t1, frames, size, size, sum / frames, worst, err);

	// The background, the three corners' colors a little inside the triangle, and its middle.
	struct { const char *name; float x, y; } probes[] = {
		{"corner (background)", 0.02f, 0.02f}, {"bottom left (red)", 0.16f, 0.13f}, {"bottom right (green)", 0.84f, 0.13f},
		{"top (blue)", 0.5f, 0.84f}, {"middle", 0.5f, 0.4f},
	};
	for (unsigned i = 0; i < sizeof probes / sizeof probes[0]; i++) {
		const unsigned char *p = px + ((int)(probes[i].y * size) * size + (int)(probes[i].x * size)) * 4;
		printf("  %-22s %3u %3u %3u %3u\n", probes[i].name, p[0], p[1], p[2], p[3]);
	}
	if (out) {
		FILE *fp = fopen(out, "wb");
		if (!fp)
			die("cannot write %s", out);
		fprintf(fp, "P6\n%d %d\n255\n", size, size);
		for (int y = size - 1; y >= 0; y--)
			for (int x = 0; x < size; x++)
				fwrite(px + (y * size + x) * 4, 1, 3, fp);
		fclose(fp);
	}
	eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
	eglDestroyContext(dpy, ctx);
	eglTerminate(dpy);
	return err != GL_NO_ERROR;
}
